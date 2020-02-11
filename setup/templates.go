package main

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/pkg/errors"
)

func renderTemplates(templateContext TemplateContext) error {
	err := filepath.Walk("..",
		func(path string, info os.FileInfo, err error) error {
			// ignore .git
			pathSplit := strings.Split(path, string(os.PathSeparator))
			if len(pathSplit) >= 2 {
				if pathSplit[1] == ".git" {
					return nil
				}
			}

			// ignore the setup pkg
			if len(pathSplit) >= 2 {
				if pathSplit[1] == "setup" {
					return nil
				}
			}

			// Ignore bin
			if len(pathSplit) >= 2 {
				if pathSplit[1] == "bin" {
					return nil
				}
			}

			fi, err := os.Stat(path)
			if err != nil {
				return errors.Wrap(err, "failed to read file info")
			}

			if fi.IsDir() {
				return nil
			}

			tmpl, err := template.ParseFiles(path)
			if err != nil {
				return errors.Wrap(err, "failed to parse template")
			}

			f, err := os.Create(path)
			if err != nil {
				return errors.Wrap(err, "failed to create file")
			}

			if err := tmpl.Execute(f, templateContext); err != nil {
				return errors.Wrap(err, "failed to execute template")
			}

			return nil
		})
	if err != nil {
		return errors.Wrap(err, "failed to walk directory")
	}

	return nil
}
