package objectstore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Store implements Store using AWS S3 (or S3-compatible services like MinIO).
type S3Store struct {
	client        *s3.Client
	presignClient *s3.PresignClient
}

// NewS3Store creates an S3Store from an existing S3 client.
func NewS3Store(client *s3.Client) *S3Store {
	return &S3Store{
		client:        client,
		presignClient: s3.NewPresignClient(client),
	}
}

func (s *S3Store) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   r,
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	_, err := s.client.PutObject(ctx, input)
	return err
}

func (s *S3Store) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s *S3Store) HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return &ObjectMeta{
		Key:          key,
		Size:         aws.ToInt64(out.ContentLength),
		ContentType:  aws.ToString(out.ContentType),
		LastModified: aws.ToTime(out.LastModified),
		ETag:         aws.ToString(out.ETag),
	}, nil
}

func (s *S3Store) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *S3Store) PresignPut(ctx context.Context, params PresignPutParams) (string, error) {
	if params.MaxSize > 0 || len(params.AllowedTypes) > 0 {
		slog.Warn("S3 presigned PUT does not enforce MaxSize or AllowedTypes server-side",
			"bucket", params.Bucket, "key", params.Key,
			"max_size", params.MaxSize, "allowed_types", params.AllowedTypes)
	}

	input := &s3.PutObjectInput{
		Bucket: aws.String(params.Bucket),
		Key:    aws.String(params.Key),
	}
	if params.ContentType != "" {
		input.ContentType = aws.String(params.ContentType)
	}

	// Pass tags as S3 object tagging.
	if len(params.Tags) > 0 {
		var tagSet []types.Tag
		for k, v := range params.Tags {
			tagSet = append(tagSet, types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
		input.Tagging = aws.String(tagsToQueryString(tagSet))
	}

	out, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(params.Expires))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return out.URL, nil
}

func (s *S3Store) PresignGet(ctx context.Context, params PresignGetParams) (string, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(params.Bucket),
		Key:    aws.String(params.Key),
	}
	if params.Filename != "" {
		input.ResponseContentDisposition = aws.String(
			fmt.Sprintf(`attachment; filename="%s"`, params.Filename),
		)
	}

	out, err := s.presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(params.Expires))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return out.URL, nil
}

func (s *S3Store) ListByPrefix(ctx context.Context, bucket, prefix string, pageSize int, pageToken string) (*ListResult, error) {
	if pageSize <= 0 {
		return s.listAllByPrefix(ctx, bucket, prefix)
	}

	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(int32(pageSize)),
	}
	if pageToken != "" {
		input.ContinuationToken = aws.String(pageToken)
	}

	out, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, err
	}

	result := &ListResult{}
	for _, obj := range out.Contents {
		result.Keys = append(result.Keys, aws.ToString(obj.Key))
	}
	if out.NextContinuationToken != nil {
		result.NextPageToken = *out.NextContinuationToken
	}
	return result, nil
}

// listAllByPrefix returns all keys matching the prefix (unpaginated, backward-compatible).
func (s *S3Store) listAllByPrefix(ctx context.Context, bucket, prefix string) (*ListResult, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return &ListResult{Keys: keys}, nil
}

func (s *S3Store) EnsureBucket(ctx context.Context, bucket string) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err == nil {
		return nil
	}
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	return err
}

// tagsToQueryString converts S3 tags to URL query string format for the Tagging header.
func tagsToQueryString(tags []types.Tag) string {
	var parts []string
	for _, t := range tags {
		parts = append(parts, url.QueryEscape(aws.ToString(t.Key))+"="+url.QueryEscape(aws.ToString(t.Value)))
	}
	return strings.Join(parts, "&")
}
