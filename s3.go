package objectstore

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	input := &s3.PutObjectInput{
		Bucket: aws.String(params.Bucket),
		Key:    aws.String(params.Key),
	}
	if params.ContentType != "" {
		input.ContentType = aws.String(params.ContentType)
	}
	out, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(params.Expires))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return out.URL, nil
}

func (s *S3Store) PresignGet(ctx context.Context, bucket, key string, expires time.Duration) (string, error) {
	out, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return out.URL, nil
}

func (s *S3Store) ListByPrefix(ctx context.Context, bucket, prefix string) ([]string, error) {
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
	return keys, nil
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
