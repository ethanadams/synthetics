// s3curl generates signed curl commands for S3 operations
package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ethanadams/synthetics/internal/executor/awsv4"
)

func main() {
	endpoint := flag.String("endpoint", os.Getenv("S3_ENDPOINT"), "S3 endpoint URL")
	accessKey := flag.String("access-key", os.Getenv("S3_ACCESS_KEY"), "S3 access key")
	secretKey := flag.String("secret-key", os.Getenv("S3_SECRET_KEY"), "S3 secret key")
	region := flag.String("region", "us-east-1", "AWS region")
	bucket := flag.String("bucket", "", "Bucket name")
	key := flag.String("key", "test-file.txt", "Object key")
	op := flag.String("op", "upload", "Operation: upload, download, delete")
	data := flag.String("data", "Hello, Storj!", "Data to upload (for upload op)")
	size := flag.Int("size", 0, "Random data size in bytes (overrides -data)")
	flag.Parse()

	if *endpoint == "" || *accessKey == "" || *secretKey == "" || *bucket == "" {
		fmt.Fprintln(os.Stderr, "Usage: s3curl -endpoint URL -access-key KEY -secret-key SECRET -bucket BUCKET [-op upload|download|delete] [-key filename] [-data content]")
		fmt.Fprintln(os.Stderr, "\nEnvironment variables: S3_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY")
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintln(os.Stderr, "  s3curl -bucket mybucket -op upload -key test.txt -data 'Hello World'")
		fmt.Fprintln(os.Stderr, "  s3curl -bucket mybucket -op download -key test.txt")
		fmt.Fprintln(os.Stderr, "  s3curl -bucket mybucket -op delete -key test.txt")
		fmt.Fprintln(os.Stderr, "  s3curl -bucket mybucket -op upload -key test.bin -size 1024")
		os.Exit(1)
	}

	creds := awsv4.Credentials{
		AccessKey: *accessKey,
		SecretKey: *secretKey,
		Region:    *region,
	}

	url := fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(*endpoint, "/"), *bucket, *key)

	var method string
	var payload []byte

	switch *op {
	case "upload":
		method = http.MethodPut
		if *size > 0 {
			payload = make([]byte, *size)
			rand.Read(payload)
			fmt.Fprintf(os.Stderr, "# Generated %d bytes of random data\n", *size)
		} else {
			payload = []byte(*data)
		}
	case "download":
		method = http.MethodGet
		payload = nil
	case "delete":
		method = http.MethodDelete
		payload = nil
	default:
		fmt.Fprintf(os.Stderr, "Unknown operation: %s\n", *op)
		os.Exit(1)
	}

	// Create request for signing
	var body *bytes.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	if payload != nil {
		req.ContentLength = int64(len(payload))
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// Sign the request
	if err := awsv4.SignRequest(req, creds, payload); err != nil {
		fmt.Fprintf(os.Stderr, "Error signing request: %v\n", err)
		os.Exit(1)
	}

	// Generate curl command
	fmt.Printf("curl -v -X %s \\\n", method)
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Printf("  -H '%s: %s' \\\n", name, value)
		}
	}

	switch *op {
	case "upload":
		if *size > 0 {
			// For large random data, suggest using dd
			fmt.Printf("  --data-binary \"$(dd if=/dev/urandom bs=%d count=1 2>/dev/null)\" \\\n", *size)
		} else {
			fmt.Printf("  --data-binary '%s' \\\n", *data)
		}
	}

	fmt.Printf("  '%s'\n", url)
}
