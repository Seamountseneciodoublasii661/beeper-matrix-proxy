package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const fallbackWaveformSamples = 100
const fallbackAvatarSize = 128

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
	uri, enc := mediaSource(content)
	data, err := nc.downloadFromLocalMatrix(ctx, uri, enc)
	if err != nil {
		return fmt.Errorf("download VCVM Matrix media: %w", err)
	}
	fileName, mimeType := mediaNameAndType(content)
	uploaded, uploadedFile, err := intent.UploadMedia(ctx, "", data, fileName, mimeType)
	if err != nil {
		return fmt.Errorf("upload media to Beeper: %w", err)
	}
	content.URL = uploaded
	content.File = uploadedFile
	nc.log.Info().
		Str("direction", "vcvm_to_beeper").
		Str("msgtype", string(content.MsgType)).
		Str("filename", fileName).
		Str("mime_type", mimeType).
		Str("mxc", string(uploaded)).
		Msg("Reuploaded media")
	return nil
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
	data, err := nc.bridge.Bot.DownloadMedia(ctx, uri, enc)
	if err != nil {
		return fmt.Errorf("download Beeper media: %w", err)
	}
	if maxSize := nc.getLocalMaxUploadSize(); maxSize > 0 && int64(len(data)) > maxSize {
		return fmt.Errorf("upload media to VCVM Matrix: file too large (%.2f MB > %.2f MB)", float64(len(data))/1000/1000, float64(maxSize)/1000/1000)
	}
	fileName, mimeType := mediaNameAndType(content)
	resp, err := nc.mx.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  mimeType,
		FileName:     fileName,
	})
	if err != nil {
		return fmt.Errorf("upload media to VCVM Matrix: %w", err)
	}
	content.URL = resp.ContentURI.CUString()
	content.File = nil
	nc.log.Info().
		Str("direction", "beeper_to_vcvm").
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
	data, err := nc.mx.DownloadBytes(ctx, uri)
	if err == nil {
		return data, nil
	}
	fallback, fallbackErr := downloadMXCDirect(ctx, uri)
	if fallbackErr != nil {
		return nil, fmt.Errorf("homeserver media proxy failed: %w; direct origin media fetch failed: %w", err, fallbackErr)
	}
	nc.log.Info().
		Str("mxc", string(uri.CUString())).
		Msg("Downloaded Matrix media directly after homeserver proxy failed")
	return fallback, nil
}

func downloadMXCDirect(ctx context.Context, uri id.ContentURI) ([]byte, error) {
	var lastErr error
	for _, downloadURL := range directMediaDownloadURLs(uri) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := matrixMediaHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := readMatrixMediaResponse(resp)
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

func readMatrixMediaResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d from direct Matrix media download", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
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
