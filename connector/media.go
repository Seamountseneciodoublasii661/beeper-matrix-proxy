package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const fallbackWaveformSamples = 100
const fallbackAvatarSize = 128
const defaultDirectMediaMaxBytes int64 = 100 * 1024 * 1024

var matrixMediaHTTPClient = &http.Client{Timeout: 60 * time.Second}

func cloneMessageContent(content *event.MessageEventContent) *event.MessageEventContent {
	if content == nil {
		return nil
	}
	raw, err := json.Marshal(content)
	if err != nil {
		dup := *content
		return &dup
	}
	var dup event.MessageEventContent
	if err = json.Unmarshal(raw, &dup); err != nil {
		shallow := *content
		return &shallow
	}
	return &dup
}

func (nc *MyNetworkClient) reuploadContentToBeeper(ctx context.Context, intent bridgev2.MatrixAPI, content *event.MessageEventContent) error {
	if content == nil {
		return nil
	}
	normalizeMediaContentForBeeper(content)
	if err := nc.reuploadMediaToBeeper(ctx, intent, content); err != nil {
		return err
	}
	if content.NewContent != nil {
		if err := nc.reuploadContentToBeeper(ctx, intent, content.NewContent); err != nil {
			return err
		}
	}
	for _, item := range content.BeeperGalleryImages {
		if err := nc.reuploadContentToBeeper(ctx, intent, item); err != nil {
			return err
		}
	}
	return nil
}

func (nc *MyNetworkClient) reuploadMediaToBeeper(ctx context.Context, intent bridgev2.MatrixAPI, content *event.MessageEventContent) error {
	if content.URL == "" && content.File == nil {
		return nil
	}
	if nc.maybeUseDirectMediaForBeeper(ctx, content) {
		return nil
	}
	uri, enc := mediaSource(content)
	data, err := nc.downloadFromLocalMatrix(ctx, uri, enc)
	if err != nil {
		return fmt.Errorf("download remote Matrix media: %w", err)
	}
	fileName, mimeType := mediaNameAndType(content)
	uploaded, uploadedFile, err := intent.UploadMedia(ctx, "", data, fileName, mimeType)
	if err != nil {
		return fmt.Errorf("upload media to Beeper: %w", err)
	}
	content.URL = uploaded
	content.File = uploadedFile
	nc.log.Info().
		Str("direction", "matrix_to_beeper").
		Str("msgtype", string(content.MsgType)).
		Str("filename", fileName).
		Str("mime_type", mimeType).
		Str("mxc", string(uploaded)).
		Msg("Reuploaded media")
	return nil
}

func (nc *MyNetworkClient) maybeUseDirectMediaForBeeper(ctx context.Context, content *event.MessageEventContent) bool {
	if nc == nil || nc.connector == nil || nc.bridge == nil || nc.login == nil || !nc.connector.directMediaEnabled() || content == nil {
		return false
	}
	if content.URL == "" || content.File != nil {
		return false
	}
	mediaID, err := encodeDirectMediaID(nc.login.ID, content.URL)
	if err != nil {
		nc.log.Warn().Err(err).Msg("Failed to encode direct media ID")
		return false
	}
	directURI, err := nc.bridge.Matrix.GenerateContentURI(ctx, mediaID)
	if err != nil {
		if !errors.Is(err, bridgev2.ErrDirectMediaNotEnabled) {
			nc.log.Warn().Err(err).Msg("Failed to generate direct media MXC")
		}
		return false
	}
	content.URL = directURI
	content.File = nil
	nc.log.Info().
		Str("direction", "matrix_to_beeper").
		Str("msgtype", string(content.MsgType)).
		Str("mxc", string(directURI)).
		Msg("Using direct media MXC")
	return true
}

func (nc *MyNetworkClient) reuploadContentToLocalMatrix(ctx context.Context, content *event.MessageEventContent) error {
	if content == nil {
		return nil
	}
	if err := nc.reuploadMediaToLocalMatrix(ctx, content); err != nil {
		return err
	}
	if content.NewContent != nil {
		if err := nc.reuploadContentToLocalMatrix(ctx, content.NewContent); err != nil {
			return err
		}
	}
	for _, item := range content.BeeperGalleryImages {
		if err := nc.reuploadContentToLocalMatrix(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (nc *MyNetworkClient) reuploadMediaToLocalMatrix(ctx context.Context, content *event.MessageEventContent) error {
	if content.URL == "" && content.File == nil {
		return nil
	}
	uri, enc := mediaSource(content)
	data, err := nc.downloadFromBeeper(ctx, uri, enc)
	if err != nil {
		return fmt.Errorf("download Beeper media: %w", err)
	}
	if maxSize := nc.getLocalMaxUploadSize(); maxSize > 0 && int64(len(data)) > maxSize {
		return fmt.Errorf("upload media to remote Matrix: file too large (%.2f MB > %.2f MB)", float64(len(data))/1000/1000, float64(maxSize)/1000/1000)
	}
	fileName, mimeType := mediaNameAndType(content)
	resp, err := nc.mx.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  mimeType,
		FileName:     fileName,
	})
	if err != nil {
		return fmt.Errorf("upload media to remote Matrix: %w", err)
	}
	content.URL = resp.ContentURI.CUString()
	content.File = nil
	nc.log.Info().
		Str("direction", "beeper_to_matrix").
		Str("msgtype", string(content.MsgType)).
		Str("filename", fileName).
		Str("mime_type", mimeType).
		Str("mxc", string(content.URL)).
		Msg("Reuploaded media")
	return nil
}

func (nc *MyNetworkClient) downloadFromLocalMatrix(ctx context.Context, uri id.ContentURIString, enc *event.EncryptedFileInfo) ([]byte, error) {
	var parsed id.ContentURI
	var err error
	if enc != nil {
		parsed, err = enc.URL.Parse()
	} else {
		parsed, err = uri.Parse()
	}
	if err != nil {
		return nil, err
	}
	data, err := nc.downloadMatrixMedia(ctx, parsed)
	if err != nil {
		return nil, err
	}
	if err := decryptEncryptedMediaInPlace(data, enc); err != nil {
		return nil, err
	}
	return data, nil
}

func (nc *MyNetworkClient) downloadMatrixMedia(ctx context.Context, uri id.ContentURI) ([]byte, error) {
	resp, err := nc.mx.Download(ctx, uri)
	if err == nil {
		data, readErr := readMatrixMediaResponse(resp, directMediaMaxBytes())
		if readErr != nil {
			err = readErr
		} else {
			return data, nil
		}
	}
	fallback, fallbackErr := downloadMXCDirect(ctx, uri, directMediaMaxBytes())
	if fallbackErr != nil {
		return nil, fmt.Errorf("homeserver media proxy failed: %w; direct origin media fetch failed: %w", err, fallbackErr)
	}
	nc.log.Info().
		Str("mxc", string(uri.CUString())).
		Msg("Downloaded Matrix media directly after homeserver proxy failed")
	return fallback, nil
}

func (nc *MyNetworkClient) downloadFromBeeper(ctx context.Context, uri id.ContentURIString, enc *event.EncryptedFileInfo) ([]byte, error) {
	var data []byte
	maxSize := nc.getLocalMaxUploadSize()
	err := nc.bridge.Bot.DownloadMediaToFile(ctx, uri, enc, false, func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if maxSize > 0 && info.Size() > maxSize {
			return fmt.Errorf("downloaded Beeper media exceeded limit (%d > %d bytes)", info.Size(), maxSize)
		}
		data, err = io.ReadAll(file)
		return err
	})
	return data, err
}

func downloadMXCDirect(ctx context.Context, uri id.ContentURI, maxBytes int64) ([]byte, error) {
	var lastErr error
	for _, url := range directMediaDownloadURLs(uri) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := matrixMediaHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := readMatrixMediaResponse(resp, maxBytes)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no direct media download URLs generated")
	}
	return nil, lastErr
}

func readMatrixMediaResponse(resp *http.Response, maxBytes int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d from direct Matrix media download", resp.StatusCode)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("Matrix media download exceeded limit by content length (%d > %d bytes)", resp.ContentLength, maxBytes)
	}
	if maxBytes <= 0 {
		return io.ReadAll(resp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("direct Matrix media download exceeded limit (%d > %d bytes)", len(data), maxBytes)
	}
	return data, nil
}

func directMediaMaxBytes() int64 {
	raw := os.Getenv("LOCAL_MATRIX_DIRECT_MEDIA_MAX_SIZE")
	if raw == "" {
		return defaultDirectMediaMaxBytes
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return defaultDirectMediaMaxBytes
	}
	return value
}

func directMediaDownloadURLs(uri id.ContentURI) []string {
	origin := "https://" + uri.Homeserver
	server := url.PathEscape(uri.Homeserver)
	mediaID := url.PathEscape(uri.FileID)
	return []string{
		origin + "/_matrix/media/v3/download/" + server + "/" + mediaID,
		origin + "/_matrix/media/r0/download/" + server + "/" + mediaID,
	}
}

func generateFallbackAvatarPNG(seed string) ([]byte, error) {
	hash := sha256.Sum256([]byte(seed))
	fill := color.RGBA{
		R: 64 + hash[0]%128,
		G: 64 + hash[1]%128,
		B: 64 + hash[2]%128,
		A: 255,
	}
	img := image.NewRGBA(image.Rect(0, 0, fallbackAvatarSize, fallbackAvatarSize))
	for y := 0; y < fallbackAvatarSize; y++ {
		for x := 0; x < fallbackAvatarSize; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mediaSource(content *event.MessageEventContent) (id.ContentURIString, *event.EncryptedFileInfo) {
	if content.File != nil {
		return content.File.URL, content.File
	}
	return content.URL, nil
}

func mediaNameAndType(content *event.MessageEventContent) (string, string) {
	fileName := content.GetFileName()
	mimeType := "application/octet-stream"
	if content.Info != nil && content.Info.MimeType != "" {
		mimeType = content.Info.MimeType
	}
	return fileName, mimeType
}

func decryptEncryptedMediaInPlace(data []byte, enc *event.EncryptedFileInfo) error {
	if enc == nil {
		return nil
	}
	return enc.DecryptInPlace(data)
}

func normalizeMediaContentForBeeper(content *event.MessageEventContent) {
	if content == nil {
		return
	}
	normalizeGIFInfo(content)
	normalizeVoiceInfo(content)
	if content.NewContent != nil {
		normalizeMediaContentForBeeper(content.NewContent)
	}
	for _, item := range content.BeeperGalleryImages {
		normalizeMediaContentForBeeper(item)
	}
}

func normalizeGIFInfo(content *event.MessageEventContent) {
	if content.Info == nil {
		return
	}
	isGIF := content.Info.MauGIF ||
		content.Info.MimeType == "image/gif" ||
		(content.MsgType == event.MsgImage && content.Info.MimeType == "image/gif")
	if !isGIF {
		return
	}
	content.Info.MauGIF = true
	if content.Info.Extra == nil {
		content.Info.Extra = make(map[string]any)
	}
	content.Info.Extra["fi.mau.loop"] = true
	content.Info.Extra["fi.mau.autoplay"] = true
	content.Info.Extra["fi.mau.hide_controls"] = true
	content.Info.Extra["fi.mau.no_audio"] = true
}

func normalizeVoiceInfo(content *event.MessageEventContent) {
	if content.MsgType != event.MsgAudio || content.MSC3245Voice == nil {
		return
	}
	if content.MSC1767Audio == nil {
		content.MSC1767Audio = &event.MSC1767Audio{}
	}
	if content.MSC1767Audio.Duration <= 0 && content.Info != nil && content.Info.Duration > 0 {
		content.MSC1767Audio.Duration = content.Info.Duration
	}
	if len(content.MSC1767Audio.Waveform) == 0 {
		content.MSC1767Audio.Waveform = make([]int, fallbackWaveformSamples)
	}
}
