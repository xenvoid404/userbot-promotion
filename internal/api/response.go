// Package api menyediakan HTTP handler, middleware, dan router untuk REST API.
package api

import (
	"encoding/json"
	"net/http"
)

// Response adalah struktur JSON standar untuk semua respons API.
// Format konsisten di semua endpoint: { "success": bool, "message"?: string, "data"?: any }
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// JSON menulis respons JSON dengan Content-Type dan status code yang diberikan.
func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

// OK menulis respons 200 OK dengan data.
func OK(w http.ResponseWriter, data any) {
	JSON(w, http.StatusOK, Response{Success: true, Data: data})
}

// Created menulis respons 201 Created dengan data.
func Created(w http.ResponseWriter, data any) {
	JSON(w, http.StatusCreated, Response{Success: true, Data: data})
}

// NoContent menulis respons 204 No Content tanpa body.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Err menulis respons error dengan status code dan pesan teks.
func Err(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, Response{Success: false, Message: msg})
}

// BadRequest menulis respons 400 Bad Request.
func BadRequest(w http.ResponseWriter, msg string) {
	Err(w, http.StatusBadRequest, msg)
}

// NotFound menulis respons 404 Not Found.
func NotFound(w http.ResponseWriter) {
	Err(w, http.StatusNotFound, "resource tidak ditemukan")
}

// InternalErr menulis respons 500 Internal Server Error.
func InternalErr(w http.ResponseWriter, err error) {
	Err(w, http.StatusInternalServerError, err.Error())
}

// Unauthorized menulis respons 401 Unauthorized.
func Unauthorized(w http.ResponseWriter) {
	Err(w, http.StatusUnauthorized, "unauthorized: token tidak valid atau tidak ada")
}
