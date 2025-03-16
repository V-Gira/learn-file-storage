package main

import (
	"fmt"
	"net/http"
	"io"
	"os"
	"path/filepath"
	"mime"
	"encoding/base64"
	"crypto/rand"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}


	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 >> 20 // 10 MB

	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form", err)
		return
	}

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error reading thumbnail", err)
		return
	}

	defer file.Close()
	
	contentType := header.Header.Get("Content-Type")
	if contentType == ""{
		respondWithError(w, http.StatusBadRequest, "Content-Type header is required", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "image/jpeg" && mediaType != "image/png") {
		respondWithError(w, http.StatusBadRequest, "Thumbnail must be a jpeg or png", err)
		return
	}

	bytes := make([]byte, 32)
	_, err = rand.Read(bytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating random data", err)
		return
	}

	fileName := base64.URLEncoding.EncodeToString(bytes)

	fileExtension := contentType[len("image/"):]

	thumbnailPath := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s.%s", fileName, fileExtension))

	thumbnail, err := os.Create(thumbnailPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating thumbnail file", err)
		return
	}

	defer thumbnail.Close()

	_, err = io.Copy(thumbnail, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying thumbnail data", err)
		return
	}

	videoMetadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting video metadata", err)
		return
	}

	if userID != videoMetadata.UserID {
		respondWithError(w, http.StatusUnauthorized, "User does not have permission to upload thumbnail for this video", nil)
		return
	}
	
	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s.%s", cfg.port, fileName, fileExtension)
	videoMetadata.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(videoMetadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoMetadata)
}
