package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/synrise25/rss-pod/internal/config"
)

type Client struct {
	client  *minio.Client
	config  config.StorageConfig
	timeout time.Duration
}

func New(cfg config.StorageConfig) (*Client, error) {
	timeout, err := cfg.TimeoutDuration()
	if err != nil {
		return nil, fmt.Errorf("parse storage timeout: %w", err)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse storage endpoint: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
		Transport:    transport,
	})
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &Client{client: client, config: cfg, timeout: timeout}, nil
}

func (c *Client) PutPrivate(ctx context.Context, key, contentType string, data []byte) error {
	return c.put(ctx, c.config.PrivateBucket, key, contentType, data)
}

func (c *Client) PutMedia(ctx context.Context, key, contentType string, data []byte) error {
	return c.put(ctx, c.config.MediaBucket, key, contentType, data)
}

func (c *Client) put(ctx context.Context, bucket, key, contentType string, data []byte) error {
	operationCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.client.PutObject(operationCtx, bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

func (c *Client) GetPrivate(ctx context.Context, key string) ([]byte, error) {
	operationCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	object, err := c.client.GetObject(operationCtx, c.config.PrivateBucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get private object %s: %w", key, err)
	}
	defer object.Close()
	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("read private object %s: %w", key, err)
	}
	return data, nil
}

func (c *Client) PublicURL(key string) string {
	return strings.TrimRight(c.config.PublicMediaBaseURL, "/") + "/" + strings.TrimLeft(key, "/")
}
