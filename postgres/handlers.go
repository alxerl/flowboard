package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"pet/task"
	"strings"
)

func (db *Database) PostHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("PostHandler", r.Method)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var currentTask task.Task
	if err := json.NewDecoder(r.Body).Decode(&currentTask); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		fmt.Println("decode error:", err)
		return
	}
	row := db.Pool.QueryRow(db.ctx, `INSERT INTO tasks(status,name) VALUES ($1,$2) RETURNING id`,
		currentTask.Status, currentTask.Name)
	var id sql.NullInt64
	if err := row.Scan(&id); err != nil {
		http.Error(w, fmt.Sprintf("insert error (scan): %v", err), http.StatusInternalServerError)
		fmt.Println("insert scan error:", err)
		return
	}
	if !id.Valid {
		http.Error(w, "insert returned NULL id", http.StatusInternalServerError)
		fmt.Println("insert returned NULL id")
		return
	}
	currentTask.Id = int(id.Int64)
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(fmt.Sprintf("Success! Your task id is: %v", currentTask.Id)))

}

func (db *Database) GetHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("GetHandler")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/list/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	var currentTask task.Task
	const query = `select name, status from tasks where id=$1`
	if err := db.Pool.QueryRow(db.ctx, query, id).Scan(&currentTask.Name, &currentTask.Status); err != nil {
		http.Error(w, fmt.Sprintf("not found or error: %v", err), http.StatusNotFound)
		return
	}
	w.Write([]byte(fmt.Sprintf("Success! Your task name:%v, status:%v", currentTask.Name, currentTask.Status)))
}

func (db *Database) DeleteHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("DeleteHandler")
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/delete/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	const query = `delete from tasks where id=$1`
	if _, err := db.Pool.Exec(db.ctx, query, id); err != nil {
		http.Error(w, fmt.Sprintf("delete error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Write([]byte("Deleted"))
}

func (db *Database) PutHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("PutHandler")
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var currentTask task.Task
	if err := json.NewDecoder(r.Body).Decode(&currentTask); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		fmt.Println("decode error:", err)
		return
	}
	if currentTask.Id == 0 {
		http.Error(w, "missing id in body", http.StatusBadRequest)
		return
	}
	const query = "update tasks set name=$1, status=$2 where id=$3"
	if _, err := db.Pool.Exec(db.ctx, query, currentTask.Name, currentTask.Status, currentTask.Id); err != nil {
		http.Error(w, fmt.Sprintf("update error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Write([]byte("Successful update!"))
}
