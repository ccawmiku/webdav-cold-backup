package storage

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type WebDAVStore struct {
	endpoint        *url.URL
	root            string
	username        string
	password        string
	client          *http.Client
	uploadBytesPS   int64
	downloadBytesPS int64
}

func NewWebDAVStore(endpoint, root, username, password string, client *http.Client) (*WebDAVStore, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid WebDAV endpoint")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("WebDAV endpoint must use HTTP or HTTPS")
	}
	if client == nil {
		client = &http.Client{}
	}
	return &WebDAVStore{
		endpoint: parsed,
		root:     cleanRemotePath(root),
		username: username,
		password: password,
		client:   client,
	}, nil
}

func (s *WebDAVStore) SetLimits(uploadMiB, downloadMiB int64) {
	s.uploadBytesPS = uploadMiB * 1024 * 1024
	s.downloadBytesPS = downloadMiB * 1024 * 1024
}

func (s *WebDAVStore) objectURL(objectPath string) *url.URL {
	copyURL := *s.endpoint
	segments := []string{}
	basePath := strings.Trim(copyURL.Path, "/")
	if basePath != "" {
		segments = append(segments, strings.Split(basePath, "/")...)
	}
	if s.root != "" {
		segments = append(segments, strings.Split(s.root, "/")...)
	}
	clean := cleanRemotePath(objectPath)
	if clean != "" {
		segments = append(segments, strings.Split(clean, "/")...)
	}
	encoded := make([]string, len(segments))
	for index, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			decoded = segment
		}
		encoded[index] = url.PathEscape(decoded)
	}
	copyURL.RawPath = "/" + strings.Join(encoded, "/")
	copyURL.Path = "/" + strings.Join(segments, "/")
	return &copyURL
}

func (s *WebDAVStore) request(ctx context.Context, method, objectPath string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, s.objectURL(objectPath).String(), body)
	if err != nil {
		return nil, err
	}
	if s.username != "" || s.password != "" {
		request.SetBasicAuth(s.username, s.password)
	}
	return request, nil
}

func (s *WebDAVStore) MkdirAll(ctx context.Context, objectPath string) error {
	if s.root != "" {
		request, err := s.request(ctx, "MKCOL", "", nil)
		if err != nil {
			return err
		}
		response, err := s.client.Do(request)
		if err != nil {
			return fmt.Errorf("create WebDAV root %q: %w", s.root, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed {
			return statusError("create root directory", s.root, response)
		}
	}
	clean := cleanRemotePath(objectPath)
	if clean == "" {
		return nil
	}
	segments := strings.Split(clean, "/")
	for index := range segments {
		current := strings.Join(segments[:index+1], "/")
		request, err := s.request(ctx, "MKCOL", current, nil)
		if err != nil {
			return err
		}
		response, err := s.client.Do(request)
		if err != nil {
			return fmt.Errorf("create WebDAV directory %q: %w", current, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed {
			return statusError("create directory", current, response)
		}
	}
	return nil
}

func (s *WebDAVStore) Put(ctx context.Context, objectPath string, source io.Reader, size int64) error {
	if err := s.MkdirAll(ctx, path.Dir(cleanRemotePath(objectPath))); err != nil {
		return err
	}
	request, err := s.request(ctx, http.MethodPut, objectPath, newLimitedReader(ctx, source, s.uploadBytesPS))
	if err != nil {
		return err
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("upload %q: %w", objectPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return statusError("upload", objectPath, response)
	}
	return nil
}

func (s *WebDAVStore) Open(ctx context.Context, objectPath string) (io.ReadCloser, error) {
	request, err := s.request(ctx, http.MethodGet, objectPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", objectPath, err)
	}
	if response.StatusCode == http.StatusNotFound {
		_ = response.Body.Close()
		return nil, ErrNotFound
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, statusError("download", objectPath, response)
	}
	return &limitedReadCloser{Reader: newLimitedReader(ctx, response.Body, s.downloadBytesPS), closer: response.Body}, nil
}

func (s *WebDAVStore) Stat(ctx context.Context, objectPath string) (Info, error) {
	items, err := s.propfind(ctx, objectPath, "0")
	if err != nil {
		return Info{}, err
	}
	if len(items) == 0 {
		return Info{}, ErrNotFound
	}
	items[0].Path = cleanRemotePath(objectPath)
	return items[0], nil
}

func (s *WebDAVStore) List(ctx context.Context, objectPath string) ([]Info, error) {
	items, err := s.propfind(ctx, objectPath, "1")
	if err != nil {
		return nil, err
	}
	base := cleanRemotePath(objectPath)
	filtered := make([]Info, 0, len(items))
	for _, item := range items {
		if cleanRemotePath(item.Path) == base {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
	return filtered, nil
}

func (s *WebDAVStore) Move(ctx context.Context, source, destination string) error {
	if err := s.MkdirAll(ctx, path.Dir(cleanRemotePath(destination))); err != nil {
		return err
	}
	request, err := s.request(ctx, "MOVE", source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Destination", s.objectURL(destination).String())
	request.Header.Set("Overwrite", "T")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("move %q: %w", source, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return statusError("move", source, response)
	}
	return nil
}

func (s *WebDAVStore) Delete(ctx context.Context, objectPath string) error {
	request, err := s.request(ctx, http.MethodDelete, objectPath, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("delete %q: %w", objectPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return statusError("delete", objectPath, response)
	}
	return nil
}

func (s *WebDAVStore) TestCompatibility(ctx context.Context) error {
	testID := ".wcb-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := s.MkdirAll(ctx, testID); err != nil {
		return fmt.Errorf("MKCOL test failed: %w", err)
	}
	defer func() { _ = s.Delete(context.Background(), testID) }()
	payload := []byte("webdav-cold-backup compatibility test")
	partial := testID + "/test.partial"
	final := testID + "/test.bin"
	if err := s.Put(ctx, partial, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return fmt.Errorf("PUT test failed: %w", err)
	}
	if info, err := s.Stat(ctx, partial); err != nil || info.Size != int64(len(payload)) {
		return fmt.Errorf("PROPFIND test failed: size mismatch")
	}
	if err := s.Move(ctx, partial, final); err != nil {
		return fmt.Errorf("MOVE test failed: %w", err)
	}
	if err := s.Delete(ctx, final); err != nil {
		return fmt.Errorf("DELETE test failed: %w", err)
	}
	return nil
}

const propfindBody = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop></d:propfind>`

type multiStatus struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href      string        `xml:"href"`
	PropStats []davPropStat `xml:"propstat"`
}

type davPropStat struct {
	Status string  `xml:"status"`
	Prop   davProp `xml:"prop"`
}

type davProp struct {
	ResourceType struct {
		Collection *struct{} `xml:"collection"`
	} `xml:"resourcetype"`
	ContentLength string `xml:"getcontentlength"`
	LastModified  string `xml:"getlastmodified"`
}

func (s *WebDAVStore) propfind(ctx context.Context, objectPath, depth string) ([]Info, error) {
	request, err := s.request(ctx, "PROPFIND", objectPath, strings.NewReader(propfindBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Depth", depth)
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", objectPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if response.StatusCode != http.StatusMultiStatus && response.StatusCode != http.StatusOK {
		return nil, statusError("list", objectPath, response)
	}
	var document multiStatus
	if err := xml.NewDecoder(io.LimitReader(response.Body, 32*1024*1024)).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode WebDAV listing: %w", err)
	}
	items := make([]Info, 0, len(document.Responses))
	for _, davItem := range document.Responses {
		var property *davProp
		for index := range davItem.PropStats {
			if strings.Contains(davItem.PropStats[index].Status, " 200 ") {
				property = &davItem.PropStats[index].Prop
				break
			}
		}
		if property == nil {
			continue
		}
		href, _ := url.PathUnescape(davItem.Href)
		itemPath := s.relativeHref(href)
		trimmed := strings.TrimSuffix(itemPath, "/")
		name := path.Base(trimmed)
		size, _ := strconv.ParseInt(property.ContentLength, 10, 64)
		modified, _ := http.ParseTime(property.LastModified)
		items = append(items, Info{Path: trimmed, Name: name, Size: size, IsDir: property.ResourceType.Collection != nil, ModifiedAt: modified})
	}
	return items, nil
}

func (s *WebDAVStore) relativeHref(href string) string {
	parsed, err := url.Parse(href)
	if err == nil && parsed.Path != "" {
		href = parsed.Path
	}
	base := strings.TrimSuffix(s.objectURL("").Path, "/")
	href = strings.TrimSuffix(href, "/")
	if strings.HasPrefix(href, base) {
		href = strings.TrimPrefix(href, base)
	}
	return cleanRemotePath(href)
}

func cleanRemotePath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	clean := path.Clean("/" + value)
	if clean == "/" || clean == "." {
		return ""
	}
	return strings.TrimPrefix(clean, "/")
}

func statusError(operation, objectPath string, response *http.Response) error {
	message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("WebDAV %s %q returned %s: %s", operation, objectPath, response.Status, strings.TrimSpace(string(message)))
}
