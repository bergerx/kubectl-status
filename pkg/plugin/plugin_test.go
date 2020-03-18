package plugin

import (
	"fmt"
	"testing"
)

func TestBasicPod(t *testing.T) {
	manifestFilename := "../../test-artifacts/pod-regular.yaml"
	out, _ := renderFile(manifestFilename)
	fmt.Println(out)
	//t.Errorf("%v", out.String())
}
