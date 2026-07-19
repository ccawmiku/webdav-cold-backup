package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/net/webdav"
)

type WebDAVServer struct {
	URL      string
	Username string
	Password string
	server   *httptest.Server
}

func NewWebDAVServer(t testing.TB) *WebDAVServer {
	t.Helper()
	username := "test-user"
	password := "test-password"
	handler := &webdav.Handler{Prefix: "/", FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	authenticated := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotUser, gotPassword, ok := request.BasicAuth()
		if !ok || gotUser != username || gotPassword != password {
			writer.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	})
	server := httptest.NewServer(authenticated)
	t.Cleanup(server.Close)
	return &WebDAVServer{URL: server.URL, Username: username, Password: password, server: server}
}
