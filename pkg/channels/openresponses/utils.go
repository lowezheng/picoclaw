package openresponses

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
)

func isImageDataURL(s string) bool {
	return strings.HasPrefix(s, "data:image/")
}

func saveDataURLToTemp(dataURL string) (string, error) {
	// Parse data URL: data:[<mediatype>][;base64],<data>
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx == -1 {
		return "", fmt.Errorf("invalid data URL")
	}
	prefix := dataURL[:commaIdx]
	data := dataURL[commaIdx+1:]

	// Extract mime type
	mimeType := "application/octet-stream"
	if strings.HasPrefix(prefix, "data:") {
		semicolonIdx := strings.Index(prefix, ";")
		if semicolonIdx == -1 {
			mimeType = strings.TrimPrefix(prefix, "data:")
		} else {
			mimeType = strings.TrimPrefix(prefix[:semicolonIdx], "data:")
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}

	ext := extFromMime(mimeType)
	tempDir := media.TempDir()
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", err
	}
	// Generate a unique filename
	filename := fmt.Sprintf("upload_%d%s", os.Getpid(), ext)
	path := filepath.Join(tempDir, filename)
	// If file exists, append a counter
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		filename = fmt.Sprintf("upload_%d_%d%s", os.Getpid(), i, ext)
		path = filepath.Join(tempDir, filename)
	}

	if err := os.WriteFile(path, decoded, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func encodeFileToDataURL(path, mimeType string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

func mimeFromExt(filename string) string {
	ext := filepath.Ext(filename)
	if ext == "" {
		return "application/octet-stream"
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		return "application/octet-stream"
	}
	return mt
}

func extFromMime(mimeType string) string {
	switch mimeType {
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	case "text/xml", "application/xml":
		return ".xml"
	case "application/javascript", "text/javascript":
		return ".js"
	case "text/css":
		return ".css"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		parts := strings.Split(mimeType, "/")
		if len(parts) == 2 {
			suffix := strings.TrimPrefix(parts[1], "x-")
			return "." + suffix
		}
		return ".bin"
	}
}

func extractRequestContent(input any) (string, []string, error) {
	if input == nil {
		return "", nil, nil
	}
	if s, ok := input.(string); ok {
		return strings.TrimSpace(s), nil, nil
	}

	var texts []string
	var mediaParts []string

	switch v := input.(type) {
	case []ContentPart:
		for _, part := range v {
			switch part.Type {
			case "input_text":
				if strings.TrimSpace(part.Content) != "" {
					texts = append(texts, part.Content)
				}
			case "input_image", "input_file":
				if strings.TrimSpace(part.Content) != "" {
					mediaParts = append(mediaParts, part.Content)
				}
			}
		}
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := m["type"].(string)
			partContent, _ := m["content"].(string)
			switch partType {
			case "input_text":
				if strings.TrimSpace(partContent) != "" {
					texts = append(texts, partContent)
				}
			case "input_image", "input_file":
				if strings.TrimSpace(partContent) != "" {
					mediaParts = append(mediaParts, partContent)
				}
			}
		}
	case []map[string]any:
		for _, m := range v {
			partType, _ := m["type"].(string)
			partContent, _ := m["content"].(string)
			switch partType {
			case "input_text":
				if strings.TrimSpace(partContent) != "" {
					texts = append(texts, partContent)
				}
			case "input_image", "input_file":
				if strings.TrimSpace(partContent) != "" {
					mediaParts = append(mediaParts, partContent)
				}
			}
		}
	default:
		return "", nil, fmt.Errorf("unsupported input type: %T", input)
	}

	content := strings.Join(texts, "\n")
	return content, mediaParts, nil
}
