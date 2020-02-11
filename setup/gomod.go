package main

import (
	"fmt"
	"io/ioutil"
	"path"
	"strings"

	"github.com/pkg/errors"
)

func renderGoMod(templateContext TemplateContext) error {
	input, err := ioutil.ReadFile(path.Join("..", "go.mod"))
	if err != nil {
		return errors.Wrap(err, "failed to read go.mod")
	}

	lines := strings.Split(string(input), "\n")

	lines[0] = fmt.Sprintf(`module github.com/%s/%s`, templateContext.Owner, templateContext.Repo)

	output := strings.Join(lines, "\n")
	err = ioutil.WriteFile(path.Join("..", "go.mod"), []byte(output), 0644)
	if err != nil {
		return errors.Wrap(err, "failed to write go.mod")
	}

	return nil
}
