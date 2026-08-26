package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/t0mer/renfild/internal/pipeline"
)

// handleUtterance is the satellite endpoint: wake + command audio in, spoken
// reply out. It answers 200 with a WAV, or 204 when there is nothing to say —
// never a 5xx, because a satellite cannot do anything useful with one.
func (s *Server) handleUtterance(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)
	if err := r.ParseMultipartForm(MaxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart body: %v", err))
		return
	}
	defer r.MultipartForm.RemoveAll()

	command, err := readPart(r, "command")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The wake snapshot is what speaker identification runs on, but a satellite
	// that cannot provide one should still be understood.
	wake, err := readPart(r, "wake")
	if err != nil {
		s.logger().Warn("utterance without wake audio", "error", err)
	}

	satelliteID := r.FormValue("satellite_id")
	if satelliteID == "" {
		satelliteID = "unknown"
	}

	response, err := s.Pipeline.Process(r.Context(), pipeline.Request{
		SatelliteID: satelliteID,
		Wake:        wake,
		Command:     command,
		Now:         time.Now(),
	})
	if err != nil {
		// Already logged and recorded by the pipeline; the response still holds
		// whatever we managed to produce.
		s.logger().Warn("utterance completed with errors", "error", err)
	}

	w.Header().Set("X-Speaker", headerValue(response.Speaker))
	w.Header().Set("X-Transcript", headerValue(response.Transcript))
	w.Header().Set("X-Intent", headerValue(response.Intent))
	w.Header().Set("X-Confidence", fmt.Sprintf("%.3f", response.Confidence))

	if len(response.Audio) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", fmt.Sprint(len(response.Audio)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(response.Audio); err != nil {
		s.logger().Warn("writing reply audio failed", "error", err)
	}
}

// readPart pulls one file part out of a multipart request.
func readPart(r *http.Request, name string) ([]byte, error) {
	file, header, err := r.FormFile(name)
	if err != nil {
		return nil, fmt.Errorf("missing %q audio part: %w", name, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxUploadBytes))
	if err != nil {
		return nil, fmt.Errorf("reading %q audio: %w", name, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%q audio is empty (%s)", name, headerName(header))
	}
	return data, nil
}

func headerName(header *multipart.FileHeader) string {
	if header == nil {
		return "no filename"
	}
	return header.Filename
}

// headerValue percent-encodes a value so non-Latin-1 text (Hebrew transcripts,
// for one) survives the trip through an HTTP header. The satellite decodes it.
func headerValue(value string) string {
	return url.PathEscape(value)
}
