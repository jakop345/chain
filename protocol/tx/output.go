package tx

import "chain/protocol/bc"

type Output struct {
	body struct {
		Source         valueSource
		ControlProgram bc.Program
		Data           bc.Hash
		ExtHash        extHash
	}
	ordinal int
}

func (Output) Type() string         { return "output1" }
func (o *Output) Body() interface{} { return o.body }

func (o Output) Ordinal() int { return o.ordinal }

func newOutput(source valueSource, controlProgram bc.Program, data bc.Hash, ordinal int) *Output {
	out := new(Output)
	out.body.Source = source
	out.body.ControlProgram = controlProgram
	out.body.Data = data
	out.ordinal = ordinal
	return out
}
