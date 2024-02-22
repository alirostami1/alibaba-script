package main

import (
	"log"

	"github.com/samber/lo"
)

type testType struct {
	Felan1 int
	Felan2 int
	Felan3 int
}

func main() {

	diff1, diff2 := lo.Difference(
		[]testType{
			{
				Felan1: 1,
				Felan2: 2,
				Felan3: 3,
			},
			{
				Felan1: 1,
				Felan2: 2,
				Felan3: 3,
			},
		},
		[]testType{
			{
				Felan1: 1,
				Felan2: 2,
				Felan3: 3,
			},
			{
				Felan1: 1,
				Felan2: 2,
				Felan3: 4,
			},
		},
	)
	log.Printf("diff1 == %#v\n", diff1)

	log.Printf("diff2 == %#v", diff2)
}
