package tx

import "chain/protocol/bc"

type Issuance struct {
	body struct {
		AnchorRef bc.Hash
		Value     bc.AssetAmount
		Data      bc.Hash
		ExtHash   extHash
	}
	ordinal int
}

func (Issuance) Type() string           { return "issuance1" }
func (iss *Issuance) Body() interface{} { return iss.body }

func (iss Issuance) Ordinal() int { return iss.ordinal }

func newIssuance(anchorRef bc.Hash, value bc.AssetAmount, data bc.Hash, ordinal int) *Issuance {
	iss := new(Issuance)
	iss.body.AnchorRef = anchorRef
	iss.body.Value = value
	iss.body.Data = data
	iss.ordinal = ordinal
	return iss
}
