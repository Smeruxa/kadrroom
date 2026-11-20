package main

import (
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"kadrroom/internal/catalogmedia"
)

const (
	maxMediaBytes = 128 << 20
	maxCoverBytes = 5 << 20
)

var (
	errUploadInvalid = errors.New("invalid upload")
	errUploadLarge   = errors.New("upload too large")
)

type uploadDraft struct {
	record   catalogmedia.Record
	filePath string
	cover    string
}

func (c *adminAPI) createDraft(w http.ResponseWriter, r *http.Request) {
	var upload uploadDraft
	defer upload.removeTemporaryFiles()
	maxBody := maxMediaBytes + maxCoverBytes + 64*1024
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBody))
	if err := readDraft(r, c.mediaDir, &upload); err != nil {
		var maxBodyError *http.MaxBytesError
		switch {
		case errors.Is(err, errUploadLarge), errors.As(err, &maxBodyError):
			catalogError(w, http.StatusRequestEntityTooLarge, "file_too_large")
		case errors.Is(err, errUploadInvalid):
			catalogError(w, http.StatusBadRequest, "invalid_upload")
		default:
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		}
		return
	}

	id, err := catalogmedia.NewID()
	if err != nil {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	upload.record.ID = id
	upload.record.Status = "processing"
	if err := c.saveDraft(r, &upload); err != nil {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, upload.record)
}

func (c *adminAPI) saveDraft(r *http.Request, upload *uploadDraft) error {
	input := filepath.Join(c.mediaDir, upload.record.ID+catalogmedia.SourceSuffix)
	if err := os.Rename(upload.filePath, input); err != nil {
		return err
	}
	upload.filePath = input
	if upload.cover != "" {
		cover := filepath.Join(c.mediaDir, upload.record.ID+catalogmedia.CoverSuffix)
		if err := os.Rename(upload.cover, cover); err != nil {
			return err
		}
		upload.cover = cover
	}
	err := c.store.Insert(r.Context(), upload.record)
	var rejected *pgconn.PgError
	definitelyRejected := pgconn.SafeToRetry(err) || (errors.As(err, &rejected) && rejected.SeverityUnlocalized == "ERROR")
	if !definitelyRejected {
		upload.filePath, upload.cover = "", ""
	}
	return err
}

func (u *uploadDraft) removeTemporaryFiles() {
	for _, path := range []string{u.filePath, u.cover} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func readDraft(r *http.Request, directory string, upload *uploadDraft) error {
	reader, err := r.MultipartReader()
	if err != nil {
		return errUploadInvalid
	}
	fields := make(map[string]bool, 5)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return malformedUpload(err)
		}
		name := part.FormName()
		if fields[name] || len(fields) == 5 {
			return errUploadInvalid
		}
		fields[name] = true

		switch name {
		case "title", "description", "mediaType":
			if part.FileName() != "" {
				return errUploadInvalid
			}
			value, err := io.ReadAll(io.LimitReader(part, 8193))
			if err != nil {
				return malformedUpload(err)
			}
			if len(value) > 8192 || !utf8.Valid(value) {
				return errUploadInvalid
			}
			upload.setField(name, strings.TrimSpace(string(value)))
		case "file":
			if part.FileName() == "" {
				return errUploadInvalid
			}
			upload.filePath, err = stageFile(directory, part, maxMediaBytes)
		case "cover":
			if part.FileName() == "" {
				return errUploadInvalid
			}
			upload.cover, err = stageFile(directory, part, maxCoverBytes)
		default:
			return errUploadInvalid
		}
		if err != nil {
			return err
		}
	}
	rule := upload.record
	if rule.Title == "" || utf8.RuneCountInString(rule.Title) > 160 ||
		utf8.RuneCountInString(rule.Description) > 2000 ||
		strings.ContainsFunc(rule.Title, unicode.IsControl) ||
		strings.ContainsFunc(rule.Description, invalidDescriptionRune) ||
		(rule.MediaType != "video" && rule.MediaType != "audio") ||
		upload.filePath == "" {
		return errUploadInvalid
	}
	if upload.cover != "" {
		upload.record.CoverType, err = checkCover(upload.cover)
	}
	return err
}

func (u *uploadDraft) setField(name, value string) {
	switch name {
	case "title":
		u.record.Title = value
	case "description":
		u.record.Description = value
	case "mediaType":
		u.record.MediaType = value
	}
}

func invalidDescriptionRune(character rune) bool {
	return unicode.IsControl(character) && character != '\n' && character != '\t' && character != '\r'
}

func malformedUpload(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return errUploadLarge
	}
	return errUploadInvalid
}

func stageFile(directory string, source io.Reader, maximum int64) (string, error) {
	file, err := os.CreateTemp(directory, ".upload-")
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maximum+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written < 1 || written > maximum {
		_ = os.Remove(file.Name())
		switch {
		case written > maximum:
			return "", errUploadLarge
		case copyErr != nil:
			var fileErr *os.PathError
			if errors.As(copyErr, &fileErr) {
				return "", copyErr
			}
			return "", malformedUpload(copyErr)
		case closeErr != nil:
			return "", closeErr
		default:
			return "", errUploadInvalid
		}
	}
	return file.Name(), nil
}

func checkCover(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	dimensions, format, err := image.DecodeConfig(file)
	if err != nil || dimensions.Width < 1 || dimensions.Height < 1 ||
		dimensions.Width > 4096 || dimensions.Height > 4096 {
		return "", errUploadInvalid
	}
	switch format {
	case "jpeg":
		return "image/jpeg", nil
	case "png":
		return "image/png", nil
	default:
		return "", errUploadInvalid
	}
}
