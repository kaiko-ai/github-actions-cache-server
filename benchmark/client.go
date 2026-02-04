package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// CacheClient handles HTTP communication with the cache server
type CacheClient struct {
	baseURL string
	client  *http.Client
}

// NewCacheClient creates a new cache client
func NewCacheClient(baseURL string) *CacheClient {
	return &CacheClient{
		baseURL: baseURL,
		client: &http.Client{
			Transport: &http.Transport{
				DisableCompression:  true,
				MaxIdleConnsPerHost: 100,
			},
			Timeout: 0, // No timeout for large transfers
		},
	}
}

// CreateCacheEntryRequest represents the request body for CreateCacheEntry
type CreateCacheEntryRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// CreateCacheEntryResponse represents the response from CreateCacheEntry
type CreateCacheEntryResponse struct {
	OK              bool   `json:"ok"`
	SignedUploadURL string `json:"signed_upload_url"`
}

// FinalizeCacheEntryRequest represents the request body for FinalizeCacheEntryUpload
type FinalizeCacheEntryRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// FinalizeCacheEntryResponse represents the response from FinalizeCacheEntryUpload
type FinalizeCacheEntryResponse struct {
	OK      bool   `json:"ok"`
	EntryID string `json:"entry_id"`
}

// GetDownloadURLRequest represents the request body for GetCacheEntryDownloadURL
type GetDownloadURLRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// GetDownloadURLResponse represents the response from GetCacheEntryDownloadURL
type GetDownloadURLResponse struct {
	OK                bool   `json:"ok"`
	SignedDownloadURL string `json:"signed_download_url"`
	MatchedKey        string `json:"matched_key"`
}

// CreateCacheEntry creates a new cache entry and returns the upload URL
func (c *CacheClient) CreateCacheEntry(key, version string) (*CreateCacheEntryResponse, error) {
	reqBody := CreateCacheEntryRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create cache entry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create cache entry failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result CreateCacheEntryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// Upload uploads data to the given upload URL
// Returns the duration of the upload in seconds
func (c *CacheClient) Upload(uploadURL string, data []byte) (float64, error) {
	// Create block ID: uuid (36 bytes) + 12-digit padded index = 48 bytes
	blockUUID := uuid.New().String()
	blockID := fmt.Sprintf("%s%012d", blockUUID, 0)
	blockIDBase64 := base64.StdEncoding.EncodeToString([]byte(blockID))

	url := fmt.Sprintf("%s?comp=block&blockid=%s", uploadURL, blockIDBase64)

	start := time.Now()

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to upload: %w", err)
	}
	defer resp.Body.Close()

	duration := time.Since(start).Seconds()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return duration, nil
}

// FinalizeCacheEntry finalizes the cache entry upload
func (c *CacheClient) FinalizeCacheEntry(key, version string) (*FinalizeCacheEntryResponse, error) {
	reqBody := FinalizeCacheEntryRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to finalize cache entry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("finalize cache entry failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result FinalizeCacheEntryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetDownloadURL gets the download URL for a cache entry
func (c *CacheClient) GetDownloadURL(key, version string) (*GetDownloadURLResponse, error) {
	reqBody := GetDownloadURLRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to get download URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get download URL failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result GetDownloadURLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// Download downloads data from the given download URL
// Returns the data and the duration in seconds
func (c *CacheClient) Download(downloadURL string) ([]byte, float64, error) {
	start := time.Now()

	resp, err := c.client.Get(downloadURL)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read response body: %w", err)
	}

	duration := time.Since(start).Seconds()
	return data, duration, nil
}

// UploadCache performs a full upload cycle (create, upload, finalize)
// Returns the upload duration in seconds
func (c *CacheClient) UploadCache(key, version string, data []byte) (float64, error) {
	// Create cache entry
	createResp, err := c.CreateCacheEntry(key, version)
	if err != nil {
		return 0, err
	}
	if !createResp.OK {
		return 0, fmt.Errorf("create cache entry returned ok=false")
	}

	// Upload data
	duration, err := c.Upload(createResp.SignedUploadURL, data)
	if err != nil {
		return 0, err
	}

	// Finalize
	_, err = c.FinalizeCacheEntry(key, version)
	if err != nil {
		return 0, err
	}

	return duration, nil
}

// DownloadCache performs a full download cycle (get URL, download)
// Returns the data and download duration in seconds
func (c *CacheClient) DownloadCache(key, version string) ([]byte, float64, error) {
	// Get download URL
	urlResp, err := c.GetDownloadURL(key, version)
	if err != nil {
		return nil, 0, err
	}
	if !urlResp.OK {
		return nil, 0, fmt.Errorf("get download URL returned ok=false")
	}

	// Download data
	return c.Download(urlResp.SignedDownloadURL)
}
