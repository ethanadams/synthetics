// Package awsv4 provides AWS Signature Version 4 request signing
// using only the Go standard library.
package awsv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	serviceName     = "s3"
	terminationStr  = "aws4_request"
	timeFormat      = "20060102T150405Z"
	dateFormat      = "20060102"
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// Credentials holds AWS credentials for signing requests.
type Credentials struct {
	AccessKey string
	SecretKey string
	Region    string
}

// Signer caches the signing key for a day to avoid repeated HMAC computation.
type Signer struct {
	creds      Credentials
	signingKey []byte
	dateStamp  string
}

// NewSigner creates a signer that caches the signing key.
func NewSigner(creds Credentials) *Signer {
	return &Signer{creds: creds}
}

// Sign signs a request using cached signing key when possible.
func (s *Signer) Sign(req *http.Request) error {
	now := time.Now().UTC()
	dateStamp := now.Format(dateFormat)

	// Refresh signing key if date changed
	if s.dateStamp != dateStamp {
		s.signingKey = deriveSigningKey(s.creds.SecretKey, dateStamp, s.creds.Region, serviceName)
		s.dateStamp = dateStamp
	}

	amzDate := now.Format(timeFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", req.Host)
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)

	canonicalReq, signedHeaders := buildCanonicalRequest(req, unsignedPayload)
	credentialScope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, s.creds.Region, serviceName, terminationStr)
	stringToSign := buildStringToSign(algorithm, amzDate, credentialScope, canonicalReq)

	// Use cached signing key
	signature := hex.EncodeToString(hmacSHA256(s.signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, s.creds.AccessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)

	return nil
}

// SignRequest signs an HTTP request using AWS Signature Version 4.
// The payload can be nil for requests without a body, or the request body bytes.
// For streaming uploads, pass nil and the request will use UNSIGNED-PAYLOAD.
func SignRequest(req *http.Request, creds Credentials, payload []byte) error {
	now := time.Now().UTC()
	return signRequestAtTime(req, creds, payload, now)
}

// SignRequestUnsigned signs a request using UNSIGNED-PAYLOAD.
// This skips hashing the payload body, which is faster for large uploads.
// The server must support unsigned payloads (most S3-compatible services do).
func SignRequestUnsigned(req *http.Request, creds Credentials) error {
	now := time.Now().UTC()
	return signRequestAtTimeUnsigned(req, creds, now)
}

func signRequestAtTimeUnsigned(req *http.Request, creds Credentials, t time.Time) error {
	amzDate := t.Format(timeFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", req.Host)
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)

	canonicalReq, signedHeaders := buildCanonicalRequest(req, unsignedPayload)

	dateStamp := t.Format(dateFormat)
	credentialScope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, creds.Region, serviceName, terminationStr)
	stringToSign := buildStringToSign(algorithm, amzDate, credentialScope, canonicalReq)

	signingKey := deriveSigningKey(creds.SecretKey, dateStamp, creds.Region, serviceName)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, creds.AccessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)

	return nil
}

// signRequestAtTime signs a request at a specific time (for testing).
func signRequestAtTime(req *http.Request, creds Credentials, payload []byte, t time.Time) error {
	// Set required headers
	amzDate := t.Format(timeFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", req.Host)

	// Calculate payload hash
	payloadHash := unsignedPayload
	if payload != nil {
		payloadHash = hashSHA256(payload)
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// Build canonical request
	canonicalReq, signedHeaders := buildCanonicalRequest(req, payloadHash)

	// Build string to sign
	dateStamp := t.Format(dateFormat)
	credentialScope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, creds.Region, serviceName, terminationStr)
	stringToSign := buildStringToSign(algorithm, amzDate, credentialScope, canonicalReq)

	// Calculate signing key
	signingKey := deriveSigningKey(creds.SecretKey, dateStamp, creds.Region, serviceName)

	// Calculate signature
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Build Authorization header
	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, creds.AccessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)

	return nil
}

// buildCanonicalRequest creates the canonical request string per AWS Sig V4 spec.
// Returns the canonical request and the signed headers string.
func buildCanonicalRequest(req *http.Request, payloadHash string) (string, string) {
	// Canonical URI (path)
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	// URL encode each path segment
	canonicalURI = canonicalURIEncode(canonicalURI)

	// Canonical query string
	canonicalQueryString := canonicalizeQueryString(req.URL.Query())

	// Canonical headers and signed headers
	canonicalHeaders, signedHeaders := canonicalizeHeaders(req.Header, req.Host)

	// Build canonical request
	canonicalReq := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	return canonicalReq, signedHeaders
}

// canonicalURIEncode encodes the URI path per AWS requirements.
func canonicalURIEncode(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// canonicalizeQueryString creates the canonical query string.
func canonicalizeQueryString(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	// Sort parameters by key
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Build canonical query string
	var parts []string
	for _, key := range keys {
		for _, value := range values[key] {
			parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalizeHeaders creates the canonical headers and signed headers strings.
func canonicalizeHeaders(headers http.Header, host string) (string, string) {
	// Headers to sign (lowercase)
	signedHeadersList := []string{"host"}

	// Collect header names
	for name := range headers {
		lowerName := strings.ToLower(name)
		// Include x-amz-* headers and content-type
		if strings.HasPrefix(lowerName, "x-amz-") || lowerName == "content-type" {
			signedHeadersList = append(signedHeadersList, lowerName)
		}
	}
	sort.Strings(signedHeadersList)

	// Build canonical headers
	var canonicalHeaderParts []string
	for _, name := range signedHeadersList {
		var value string
		if name == "host" {
			value = host
		} else {
			// Get the header value (case-insensitive lookup)
			for hName, hValues := range headers {
				if strings.ToLower(hName) == name && len(hValues) > 0 {
					value = strings.TrimSpace(hValues[0])
					break
				}
			}
		}
		canonicalHeaderParts = append(canonicalHeaderParts, name+":"+value+"\n")
	}

	canonicalHeaders := strings.Join(canonicalHeaderParts, "")
	signedHeaders := strings.Join(signedHeadersList, ";")

	return canonicalHeaders, signedHeaders
}

// buildStringToSign creates the string to sign.
func buildStringToSign(algorithm, amzDate, credentialScope, canonicalRequest string) string {
	return strings.Join([]string{
		algorithm,
		amzDate,
		credentialScope,
		hashSHA256([]byte(canonicalRequest)),
	}, "\n")
}

// deriveSigningKey derives the signing key using HMAC chain.
func deriveSigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte(terminationStr))
	return kSigning
}

// hashSHA256 computes the SHA256 hash of data and returns hex string.
func hashSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// hmacSHA256 computes HMAC-SHA256 of data using the given key.
func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// SignRequestStreaming signs a request for streaming upload.
// It uses UNSIGNED-PAYLOAD since the body is streamed.
func SignRequestStreaming(req *http.Request, creds Credentials) error {
	return SignRequest(req, creds, nil)
}

// HashPayload computes the SHA256 hash of a reader's content.
// Useful for pre-computing payload hash for signed uploads.
func HashPayload(r io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
