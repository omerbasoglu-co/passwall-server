package api

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pass-wall/passwall-server/internal/app"
	"github.com/pass-wall/passwall-server/internal/storage"
	"github.com/pass-wall/passwall-server/model"
	"github.com/spf13/viper"
)

// Import ...
func Import(s storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url := r.FormValue("url")
		username := r.FormValue("username")
		password := r.FormValue("password")

		uploadedFile, err := upload(r)
		if err != nil {
			RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer uploadedFile.Close()

		// Go to first line of file
		uploadedFile.Seek(0, 0)

		// Read file content and add logins to db
		err = app.InsertValues(s, url, username, password, uploadedFile)
		if err != nil {
			RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Delete imported file
		err = os.Remove(uploadedFile.Name())
		if err != nil {
			RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		response := model.Response{http.StatusOK, "Success", "Import finished successfully!"}
		RespondWithJSON(w, http.StatusOK, response)
	}
}

// Export exports all logins as CSV file
func Export(s storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		var logins []model.Login
		s.Find(&logins)

		logins = app.DecryptLoginPasswords(logins)

		content := [][]string{}
		content = append(content, []string{"URL", "Username", "Password"})
		for i := range logins {
			content = append(content, []string{logins[i].URL, logins[i].Username, logins[i].Password})
		}

		b := &bytes.Buffer{} // creates IO Writer
		csvWriter := csv.NewWriter(b)
		strWrite := content
		csvWriter.WriteAll(strWrite)
		csvWriter.Flush()

		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment;filename=PassWall.csv")
		w.Write(b.Bytes())
	}
}

// Restore restores logins from backup file ./store/passwall-{BACKUP_DATE}.bak
func Restore(s storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var restoreDTO model.RestoreDTO

		// get restoreDTO
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&restoreDTO); err != nil {
			RespondWithError(w, http.StatusUnprocessableEntity, "Invalid json provided")
			return
		}
		defer r.Body.Close()

		backupFolder := viper.GetString("backup.folder")
		backupFile := restoreDTO.Name
		// add extension if there is no
		if len(filepath.Ext(restoreDTO.Name)) <= 0 {
			backupFile = fmt.Sprintf("%s%s", restoreDTO.Name, ".bak")
		}
		backupPath := filepath.Join(backupFolder, backupFile)

		_, err := os.Open(backupPath)
		if err != nil {
			RespondWithError(w, http.StatusNotFound, err.Error())
			return
		}

		loginsByte := app.DecryptFile(backupPath, viper.GetString("server.passphrase"))

		var loginDTOs []model.LoginDTO
		json.Unmarshal(loginsByte, &loginDTOs)

		for i := range loginDTOs {

			login := model.Login{
				URL:      loginDTOs[i].URL,
				Username: loginDTOs[i].Username,
				Password: base64.StdEncoding.EncodeToString(app.Encrypt(loginDTOs[i].Password, viper.GetString("server.passphrase"))),
			}

			s.Logins().Save(login)
		}

		response := model.Response{http.StatusOK, "Success", "Restore from backup completed successfully!"}
		RespondWithJSON(w, http.StatusOK, response)
	}
}

// Backup backups the store
func Backup(s storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := app.BackupData(s)

		if err != nil {
			RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		response := model.Response{http.StatusOK, "Success", "Backup completed successfully!"}
		RespondWithJSON(w, http.StatusOK, response)
	}
}

// ListBackup all backups
func ListBackup(w http.ResponseWriter, r *http.Request) {
	backupFiles, err := app.GetBackupFiles()

	if err != nil {
		RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var response []model.Backup
	for _, backupFile := range backupFiles {
		response = append(response, model.Backup{Name: backupFile.Name(), CreatedAt: backupFile.ModTime()})
	}

	RespondWithJSON(w, http.StatusOK, response)
}

// MigrateTables runs auto migration for the models, will only add missing fields
// won't delete/change current data in the store.
func MigrateTables(s storage.Store) {
	if err := s.Logins().Migrate(); err != nil {
		log.Println(err)
	}
	if err := s.CreditCards().Migrate(); err != nil {
		log.Println(err)
	}
	if err := s.BankAccounts().Migrate(); err != nil {
		log.Println(err)
	}
	if err := s.Notes().Migrate(); err != nil {
		log.Println(err)
	}
	if err := s.Tokens().Migrate(); err != nil {
		log.Println(err)
	}
}

/* func MigrateTables(s storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.Logins().Migrate(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := s.CreditCards().Migrate(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := s.BankAccounts().Migrate(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := s.Notes().Migrate(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
} */

func upload(r *http.Request) (*os.File, error) {

	// Max 10 MB
	r.ParseMultipartForm(10 << 20)

	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	ext := filepath.Ext(header.Filename)

	if ext != ".csv" {
		return nil, fmt.Errorf("%s unsupported filetype", ext)
	}

	tempFile, err := ioutil.TempFile("/tmp", "passwall-import-*.csv")
	if err != nil {
		return nil, err
	}

	fileBytes, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}

	tempFile.Write(fileBytes)

	return tempFile, err
}
