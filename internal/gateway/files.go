package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/files"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
)

func (g *Gateway) handleFileUploadRequest(sess *session.Session, env protocol.Envelope) {
	var req protocol.FileUploadRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	ticket, err := g.files.RequestUpload(sess, req)
	if err != nil {
		g.replyFileError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeFileUploadTicket, env.ID, ticket)
}

func (g *Gateway) handleFileDownloadToken(sess *session.Session, env protocol.Envelope) {
	var req protocol.FileDownloadRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "محتوای پیام معتبر نیست")
		return
	}

	link, err := g.files.IssueDownload(sess, req.FileID)
	if err != nil {
		g.replyFileError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeFileDownload, env.ID, link)
}

// AnnounceUpload posts a finished upload as a message in the uploader's room.
// The HTTP upload handler calls it once the bytes are safely on disk.
func (g *Gateway) AnnounceUpload(uploader *session.Session, file files.File) {
	if uploader == nil {
		return
	}
	if _, err := g.chat.PostAttachment(uploader, file.Attachment()); err != nil {
		slog.Error("announcing upload failed", "file", file.ID, "err", err)
	}
}

func (g *Gateway) replyFileError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, files.ErrDisabled):
		sess.SendError(id, protocol.ErrUploadsDisabled, "ارسال فایل در این سرور غیرفعال است")
	case errors.Is(err, files.ErrForbidden):
		sess.SendError(id, protocol.ErrForbidden, "اجازهٔ ارسال فایل را ندارید")
	case errors.Is(err, files.ErrNotInRoom):
		sess.SendError(id, protocol.ErrNotInRoom, "برای ارسال فایل باید داخل یک روم باشید")
	case errors.Is(err, files.ErrTooLarge):
		sess.SendError(id, protocol.ErrFileTooLarge, "فایل بزرگ‌تر از حد مجاز است")
	case errors.Is(err, files.ErrQuotaFull):
		sess.SendError(id, protocol.ErrRoomQuotaFull, "سهمیهٔ فایل این روم پر است")
	case errors.Is(err, files.ErrNotFound):
		sess.SendError(id, protocol.ErrFileNotFound, "این فایل دیگر در دسترس نیست")
	case errors.Is(err, files.ErrNameRequired):
		sess.SendError(id, protocol.ErrFileInvalid, "نام فایل معتبر نیست")
	default:
		slog.Error("file operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "خطای داخلی سرور")
	}
}
