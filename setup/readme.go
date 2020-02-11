package main

import (
	"io/ioutil"
	"path"

	"github.com/pkg/errors"
)

func renderReadme(templateContext TemplateContext) error {
	input, err := ioutil.ReadFile(path.Join("hack", "README.txt"))
	if err != nil {
		return errors.Wrap(err, "failed to read README.txt")
	}

	err = ioutil.WriteFile(path.Join("..", "README.md"), input, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to write README.md")
	}

	return nil
}
