package httpapi

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"clawbench-fleet/internal/fleet"
)

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	rows, err := parseImportCSV(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_csv", err.Error())
		return
	}
	preview := s.svc.PreviewImport(rows)
	invalid := 0
	for _, row := range preview {
		if row.Error != "" {
			invalid++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": preview, "total": len(preview), "importable": len(preview) - invalid, "invalid": invalid,
	})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	rows, err := parseImportCSV(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_csv", err.Error())
		return
	}
	job, err := s.svc.ImportRows(r.Context(), rows, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{fieldJob: job})
}

func parseImportCSV(w http.ResponseWriter, r *http.Request) ([]fleet.ImportRow, error) {
	data, err := readUploadCSV(w, r)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err != nil {
		return nil, errors.New("CSV 是空的或格式不对")
	}
	index := map[string]int{}
	for i, h := range header {
		index[strings.ToLower(strings.TrimSpace(h))] = i
	}
	userIdx, ok := index["username"]
	if !ok {
		return nil, errors.New("CSV 缺少 username 列")
	}
	displayIdx, hasDisplay := index["display_name"]
	notesIdx, hasNotes := index["notes"]
	return readImportRows(reader, userIdx, displayIdx, hasDisplay, notesIdx, hasNotes)
}

// readUploadCSV reads the multipart "file" field, caps its size and strips a
// UTF-8 BOM (Excel writes one and it would corrupt the header name).
func readUploadCSV(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return nil, fmt.Errorf("读取上传文件失败：%w", err)
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, errors.New("请选择一个 CSV 文件")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		return nil, err
	}
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), nil
}

// readImportRows turns the body lines after the header into import rows. Blank
// lines are skipped; more than 1000 rows is rejected before any work starts.
func readImportRows(reader *csv.Reader, userIdx, displayIdx int, hasDisplay bool, notesIdx int, hasNotes bool) ([]fleet.ImportRow, error) {
	var rows []fleet.ImportRow
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSV 第 %d 行解析失败：%w", len(rows)+2, err)
		}
		row := fleet.ImportRow{Username: fieldAt(record, userIdx)}
		if hasDisplay {
			row.DisplayName = fieldAt(record, displayIdx)
		}
		if hasNotes {
			row.Notes = fieldAt(record, notesIdx)
		}
		if row.Username == "" && row.DisplayName == "" && row.Notes == "" {
			continue
		}
		rows = append(rows, row)
		if len(rows) > 1000 {
			return nil, errors.New("一次最多导入 1000 行")
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("CSV 里没有数据行")
	}
	return rows, nil
}

// fieldAt returns a trimmed column, or "" when the row is short.
func fieldAt(record []string, idx int) string {
	if idx < 0 || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}
