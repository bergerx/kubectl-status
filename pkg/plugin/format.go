package plugin

import (
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cast"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
)

func (cfg *RenderConfig) ip(ip string) string {
	if cfg.Viper.GetBool("test-hack") {
		return "1.1.1.1"
	}
	return ip
}

func addFloat64(i ...interface{}) float64 {
	var a float64 = 0
	for _, b := range i {
		a += cast.ToFloat64(b)
	}
	return a
}

func subFloat64(a, b float64) float64 {
	return b - a
}

func divFloat64(a, b float64) float64 {
	return b / a
}

func humanizeSI(unit string, input float64) string {
	return strings.Replace(humanize.SIWithDigits(input, 1, unit), " ", "", -1)
}

// humanizeSIPair renders two related values (e.g. allocatable/capacity) under a single shared SI
// unit, scaled to the larger of the two, e.g. humanizeSIPair("B", 32.8e9, 33.6e9) -> "32.8/33.6GB".
func humanizeSIPair(unit string, a, b float64) string {
	scaledB, prefix := humanize.ComputeSI(b)
	scale := 1.0
	if scaledB != 0 {
		scale = b / scaledB
	}
	return fmt.Sprintf("%s/%s%s", humanize.FtoaWithDigits(a/scale, 1), humanize.FtoaWithDigits(scaledB, 1), prefix+unit)
}

func quantityToFloat64(str string) float64 {
	quantity, _ := resource2.ParseQuantity(str)
	return float64(quantity.MilliValue()) / 1000
}

func quantityToInt64(str string) int64 {
	quantity, _ := resource2.ParseQuantity(str)
	return quantity.Value()
}

func percent(x, y float64) float64 {
	return x / y * 100
}
