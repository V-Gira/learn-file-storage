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
	"os/exec"
	"encoding/json"
	"bytes"
	"log"

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

	tempPath := video.Name()

	processedPath, err := processVideoForFastStart(tempPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}
	if !checkMoovAtom(processedPath) {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}

	processedVideo, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed video", err)
		return
	}
	
	defer os.Remove(tempPath)
	defer os.Remove(processedPath)
	defer processedVideo.Close()
	

	aspectRatio, err := getVideoAspectRatio(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting video aspect ratio", err)
		return
	}

	_, err = processedVideo.Seek(0, io.SeekStart)
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
	key := fmt.Sprintf("%s/%s.mp4", aspectRatio, fileName)

	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key:    &key,
		Body:   processedVideo,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading video to S3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution, key)

	videoMetadata.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(videoMetadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoMetadata)
}

type Stream struct {
    Width  int `json:"width"`
    Height int `json:"height"`
}

type FFProbeOutput struct {
    Streams []Stream `json:"streams"`
}

func getVideoAspectRatio(filePath string) (string, error) {
    // Run the ffprobe command
    cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	output, err := cmd.CombinedOutput() // Capture both stdout and stderr
    if err != nil {
        return "", fmt.Errorf("failed to run ffprobe: %w\n%s", err, string(output))
    }

    // Parse the JSON output
    var ffprobeOutput FFProbeOutput
    err = json.Unmarshal(output, &ffprobeOutput)
    if err != nil {
        return "", fmt.Errorf("failed to parse ffprobe output: %w", err)
    }

    if len(ffprobeOutput.Streams) == 0 {
        return "", fmt.Errorf("no video streams found in file: %s", filePath)
    }

    // Calculate the aspect ratio
    width := ffprobeOutput.Streams[0].Width
    height := ffprobeOutput.Streams[0].Height

	is16by9 := width/16 == height/9
	is9by16 := width/9 == height/16

	// asspectRatio is either "16:9" or "9:16" or "other"
	aspectRatio := "other"
	if is16by9 {
		aspectRatio = "landscape"
	} else if is9by16 {
		aspectRatio = "portrait"
	}

    return aspectRatio, nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := fmt.Sprintf("%s.processing", filePath)
	// Run the ffmpeg command
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	output, err := cmd.CombinedOutput() // Capture both stdout and stderr
	if err != nil {
		return "", fmt.Errorf("failed to run ffmpeg: %w\n%s", err, string(output))
	}

	return outputPath, nil
}

func checkMoovAtom(filePath string) bool {
    file, err := os.Open(filePath)
    if err != nil {
        log.Printf("Error opening file to check moov: %v", err)
        return false
    }
    defer file.Close()
    
    // Read first 200 bytes
    buffer := make([]byte, 200)
    _, err = file.Read(buffer)
    if err != nil {
        log.Printf("Error reading file: %v", err)
        return false
    }
    
    // Check if "moov" is in those bytes
    return bytes.Contains(buffer, []byte("moov"))
}