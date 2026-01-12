package storj

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"go.k6.io/k6/js/modules"
	"storj.io/uplink"
)

func init() {
	modules.Register("k6/x/storj", new(Storj))
}

// Storj is the k6 extension for Storj operations
type Storj struct{}

// Client represents a Storj uplink client
type Client struct {
	access  *uplink.Access
	project *uplink.Project
}

// NewClient creates a new Storj client from an access grant
func (s *Storj) NewClient(accessGrant string) (*Client, error) {
	if accessGrant == "" {
		return nil, errors.New("access grant is required")
	}

	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, err
	}

	return &Client{
		access:  access,
		project: project,
	}, nil
}

// Upload uploads data to a Storj bucket with optional TTL
// ttlSeconds: if > 0, object will expire after this many seconds
func (c *Client) Upload(bucketName, key string, data []byte, ttlSeconds int) error {
	if c.project == nil {
		return errors.New("client not initialized")
	}

	ctx := context.Background()

	// Ensure bucket exists
	_, err := c.project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return err
	}

	// Prepare upload options
	var opts *uplink.UploadOptions
	if ttlSeconds > 0 {
		opts = &uplink.UploadOptions{
			Expires: time.Now().Add(time.Duration(ttlSeconds) * time.Second),
		}
	}

	// Start upload
	upload, err := c.project.UploadObject(ctx, bucketName, key, opts)
	if err != nil {
		return err
	}
	defer upload.Abort()

	// Write data
	_, err = io.Copy(upload, bytes.NewReader(data))
	if err != nil {
		return err
	}

	// Commit upload
	return upload.Commit()
}

// Download downloads data from a Storj bucket
func (c *Client) Download(bucketName, key string) ([]byte, error) {
	if c.project == nil {
		return nil, errors.New("client not initialized")
	}

	ctx := context.Background()

	// Start download
	download, err := c.project.DownloadObject(ctx, bucketName, key, nil)
	if err != nil {
		return nil, err
	}
	defer download.Close()

	// Read all data
	data, err := io.ReadAll(download)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// List lists objects in a Storj bucket
func (c *Client) List(bucketName string) ([]string, error) {
	if c.project == nil {
		return nil, errors.New("client not initialized")
	}

	ctx := context.Background()

	// List objects
	objects := c.project.ListObjects(ctx, bucketName, nil)

	var keys []string
	for objects.Next() {
		item := objects.Item()
		keys = append(keys, item.Key)
	}

	if err := objects.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

// Delete deletes an object from a Storj bucket
func (c *Client) Delete(bucketName, key string) error {
	if c.project == nil {
		return errors.New("client not initialized")
	}

	ctx := context.Background()

	_, err := c.project.DeleteObject(ctx, bucketName, key)
	return err
}

// Stat gets object metadata
func (c *Client) Stat(bucketName, key string) (map[string]interface{}, error) {
	if c.project == nil {
		return nil, errors.New("client not initialized")
	}

	ctx := context.Background()

	object, err := c.project.StatObject(ctx, bucketName, key)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"key":       object.Key,
		"size":      object.System.ContentLength,
		"created":   object.System.Created.Unix(),
		"is_prefix": object.IsPrefix,
	}, nil
}

// Close closes the Storj project connection
func (c *Client) Close() error {
	if c.project == nil {
		return nil
	}
	return c.project.Close()
}
