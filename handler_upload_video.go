package main

import (
	"net/http"
	"io"
	"os"
	"mime"
	"encoding/base64"
	"crypto/rand"
	"fmt"
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30 // 1 GB

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

	videoMetadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting video metadata", err)
		return
	}

	if userID != videoMetadata.UserID {
		respondWithError(w, http.StatusUnauthorized, "User does not have permission to upload thumbnail for this video", nil)
		return
	}

	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error reading video", err)
		return
	}

	defer file.Close()
	
	contentType := header.Header.Get("Content-Type")
	if contentType == ""{
		respondWithError(w, http.StatusBadRequest, "Content-Type header is required", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "video/mp4") {
		respondWithError(w, http.StatusBadRequest, "Video must be an mp4", err)
		return
	}

	tempName := "tubely-temp.mp4"

	video, err := os.CreateTemp("", tempName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return
	}

	defer os.Remove(tempName)
	defer video.Close()

	_, err = io.Copy(video, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video data", err)
		return
	}

	_, err = video.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error seeking video data", err)
		return
	}

	bytes := make([]byte, 32)
	_, err = rand.Read(bytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating random data", err)
		return
	}

	fileName := base64.URLEncoding.EncodeToString(bytes)
	//key is filename + mp4
	key := fmt.Sprintf("%s.mp4", fileName)

	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key:    &key,
		Body:   video,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading video to S3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)

	videoMetadata.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(videoMetadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoMetadata)
}
