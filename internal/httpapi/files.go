package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"tamizchat/internal/files"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

// Files is the upload/download surface the HTTP layer needs.
type Files interface {
	Upload(ctx context.Context, token string, body io.Reader) (files.File, *session.Session, error)
	Open(fileID, token string, thumb bool) (files.File, *os.File, error)
}

// handleUpload receives the bytes of a file the client already got a ticket
// for. The ticket carries the room, the uploader and the size limit, so this
// handler needs no separate authentication.
func (d Deps) handleUpload(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized,
			errorBody(protocol.ErrBadRequest, "توکن آپلود لازم است"))
		return
	}

	// A hard ceiling on the request body regardless of the ticket, so a client
	// cannot stream forever into a handler.
	r.Body = http.MaxBytesReader(w, r.Body, d.MaxUploadBytes()+(1<<20))

	file, uploader, err := d.Files.Upload(r.Context(), token, r.Body)
	if err != nil {
		status, code, message := uploadError(err)
		writeJSON(w, status, errorBody(code, message))
		return
	}

	// The file becomes a message in the room, which is how everyone finds out
	// about it — an upload nobody is told about would be pointless.
	if d.OnUpload != nil {
		d.OnUpload(uploader, file)
	}

	writeJSON(w, http.StatusOK, file.Attachment())
}

// handleDownload serves a file, or its thumbnail, to a holder of a valid link.
func (d Deps) handleDownload(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("id")
	token := bearerToken(r)
	thumb := r.URL.Query().Get("thumb") == "1"

	file, body, err := d.Files.Open(fileID, token, thumb)
	if err != nil {
		status := http.StatusNotFound
		code := protocol.ErrFileNotFound
		if errors.Is(err, files.ErrBadToken) {
			status, code = http.StatusForbidden, protocol.ErrForbidden
		}
		writeJSON(w, status, errorBody(code, "این فایل در دسترس نیست"))
		return
	}
	defer body.Close()

	// Never let the browser or an embedded webview decide what these bytes are:
	// an uploaded file must not be able to execute as a page on the server's
	// origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")

	if thumb {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Type", file.MIME)
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
		w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	}

	if _, err := io.Copy(w, body); err != nil {
		slog.Debug("file download interrupted", "file", fileID, "err", err)
	}
}

// bearerToken reads the token from the query string or the Authorization
// header. The query string is what the ticket URL uses; the header is there for
// clients that would rather not put a credential in a URL.
func bearerToken(r *http.Request) string {
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}
	const prefix = "Bearer "
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	}
	return ""
}

// uploadError maps a storage failure onto an HTTP status and a protocol code.
func uploadError(err error) (int, string, string) {
	switch {
	case errors.Is(err, files.ErrBadTicket):
		return http.StatusForbidden, protocol.ErrForbidden, "توکن آپلود معتبر نیست یا منقضی شده"
	case errors.Is(err, files.ErrNotInRoom):
		return http.StatusConflict, protocol.ErrNotInRoom, "دیگر در آن روم نیستید"
	case errors.Is(err, files.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, protocol.ErrFileTooLarge, "فایل بزرگ‌تر از حد مجاز است"
	case errors.Is(err, files.ErrQuotaFull):
		return http.StatusInsufficientStorage, protocol.ErrRoomQuotaFull, "سهمیهٔ فایل این روم پر است"
	case errors.Is(err, files.ErrEmptyUpload):
		return http.StatusBadRequest, protocol.ErrFileInvalid, "فایل خالی است"
	default:
		slog.Error("upload failed", "err", err)
		return http.StatusInternalServerError, protocol.ErrInternal, "خطای داخلی سرور"
	}
}

func errorBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}
