package server

import (
	"net/http"
	"pet/postgres"
)

func HandlePool(dtb postgres.Database) {
	http.HandleFunc("/create", dtb.PostHandler)
	http.HandleFunc("/delete", dtb.DeleteHandler)
	http.HandleFunc("/list/", dtb.GetHandler)
	http.HandleFunc("/done", dtb.PutHandler)
}
