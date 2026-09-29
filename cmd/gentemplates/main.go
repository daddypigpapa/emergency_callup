// Command gentemplates (re)writes the standard spreadsheet templates into
// templates/. Run from the repo root: `make templates`.
package main

import (
	"log"
	"os"
	"path/filepath"

	"emergencycallup/internal/templatebuild"
)

func main() {
	for _, t := range templatebuild.All {
		data, err := t.Build()
		if err != nil {
			log.Fatalf("%s: %v", t.FileName, err)
		}
		path := filepath.Join("templates", t.FileName)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Fatalf("%s: %v", path, err)
		}
		log.Printf("wrote %s", path)
	}
}
