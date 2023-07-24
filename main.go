package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/jsonpb"
	"github.com/jaremko/a7p_transfer_example/profedit"
)

func jsonToProto(jsonStr string, pb proto.Message) error {
	return jsonpb.UnmarshalString(jsonStr, pb)
}

func protoToJson(pb proto.Message) (string, error) {
	marshaler := jsonpb.Marshaler{EmitDefaults: true}
	return marshaler.MarshalToString(pb)
}

func checksum(data []byte) string {
	h := md5.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func validateAndStripChecksum(data []byte) ([]byte, error) {
	if len(data) <= 32 {
		return nil, fmt.Errorf("data too short for a checksum")
	}
	prefix, content := data[:32], data[32:]
	calculatedChecksum := checksum(content)
	if string(prefix) != calculatedChecksum {
		return nil, fmt.Errorf("checksum mismatch: expected %s, got %s", calculatedChecksum, string(prefix))
	}
	return content, nil
}

func sanitizeFilename(filename string) (string, error) {
	if strings.Contains(filename, "..") || !strings.HasSuffix(filename, ".a7p") {
		return "", errors.New("invalid filename")
	}
	return filename, nil
}

func respondWithError(w http.ResponseWriter, code int, message string) {
	resp, _ := json.Marshal(map[string]string{"error": message})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(resp)
}

func handleStaticFiles(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, r.URL.Path[1:])
}

func handleFileList(dir string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondWithError(w, http.StatusMethodNotAllowed, "Invalid request method")
		return
	}

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		log.Printf("Error reading directory: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	var fileNames []string
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".a7p") {
			fileNames = append(fileNames, file.Name())
		}
	}

	fileListJson, err := json.Marshal(fileNames)
	if err != nil {
		log.Printf("Error marshalling file list to JSON: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	w.Write(fileListJson)
}

func handleGetFile(dir string, w http.ResponseWriter, r *http.Request) {
	filename, err := sanitizeFilename(r.URL.Query().Get("filename"))
	if err != nil {
		log.Printf("Invalid filename: %v", err)
		respondWithError(w, http.StatusBadRequest, "Invalid filename")
		return
	}

	data, err := ioutil.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		log.Printf("Error reading file: %v", err)
		respondWithError(w, http.StatusNotFound, "File not found")
		return
	}

	content, err := validateAndStripChecksum(data)
	if err != nil {
		log.Printf("Error validating or stripping checksum: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	pb := &profedit.Payload{}
	err = proto.Unmarshal(content, pb)
	if err != nil {
		log.Printf("Error unmarshalling proto file: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	jsonStr, err := protoToJson(pb)
	if err != nil {
		log.Printf("Error marshalling proto file to json: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	w.Write([]byte(jsonStr))
}

func handlePutFile(dir string, w http.ResponseWriter, r *http.Request) {
	filename, err := sanitizeFilename(r.URL.Query().Get("filename"))
	if err != nil {
		log.Printf("Invalid filename: %v", err)
		respondWithError(w, http.StatusBadRequest, "Invalid filename")
		return
	}

	var req struct {
		Content json.RawMessage `json:"content"`
	}

	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("Error decoding request body: %v", err)
		respondWithError(w, http.StatusBadRequest, "Bad request")
		return
	}

	pb := &profedit.Payload{}
	err = jsonToProto(string(req.Content), pb)
	if err != nil {
		log.Printf("Error unmarshalling json to proto: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	content, err := proto.Marshal(pb)
	if err != nil {
		log.Printf("Error marshalling proto to bytes: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	checksum := checksum(content)
	data := append([]byte(checksum), content...)

	err = ioutil.WriteFile(filepath.Join(dir, filename), data, 0644)
	if err != nil {
		log.Printf("Error writing file: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Server error")
		return
	}

	w.Write([]byte("OK"))
}

func handleDeleteFile(dir string, w http.ResponseWriter, r *http.Request) {
	filename, err := sanitizeFilename(r.URL.Query().Get("filename"))
	if err != nil {
		log.Printf("Invalid filename: %v", err)
		respondWithError(w, http.StatusBadRequest, "Invalid filename")
		return
	}

	err = os.Remove(filepath.Join(dir, filename))
	if err != nil {
		log.Printf("Error deleting file: %v", err)
		respondWithError(w, http.StatusNotFound, "File not found")
		return
	}

	w.Write([]byte("OK"))
}

func main() {
	dirPtr := flag.String("dir", ".", "directory to serve")
	certPtr := flag.String("cert", "cert.pem", "path to the certificate file")
	keyPtr := flag.String("key", "key.pem", "path to the key file")

	flag.Parse()

	log.Printf("Starting localhost server at https://localhost/")
	log.Printf("You might want to integrate https://github.com/FiloSottile/mkcert")
	log.Printf("If \"unknown certificate message\" annoys you too much.")

	http.HandleFunc("/", handleStaticFiles)
	http.HandleFunc("/filelist", func(w http.ResponseWriter, r *http.Request) { handleFileList(*dirPtr, w, r) })
	http.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleGetFile(*dirPtr, w, r)
		case http.MethodPut:
			handlePutFile(*dirPtr, w, r)
		case http.MethodDelete:
			handleDeleteFile(*dirPtr, w, r)
		default:
			respondWithError(w, http.StatusMethodNotAllowed, "Invalid request method")
		}
	})

	log.Fatal(http.ListenAndServeTLS(":443", *certPtr, *keyPtr, nil))
}