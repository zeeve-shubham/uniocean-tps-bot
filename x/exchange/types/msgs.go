package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// MsgDeposit
func (msg *MsgDeposit) GetSigners() []sdk.AccAddress {
	addr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{addr}
}

func (msg *MsgDeposit) ValidateBasic() error { return nil }

// MsgCreateSpotLimitOrder
func (msg *MsgCreateSpotLimitOrder) GetSigners() []sdk.AccAddress {
	addr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{addr}
}

func (msg *MsgCreateSpotLimitOrder) ValidateBasic() error { return nil }

// MsgCreateDerivativeLimitOrder
func (msg *MsgCreateDerivativeLimitOrder) GetSigners() []sdk.AccAddress {
	addr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{addr}
}

func (msg *MsgCreateDerivativeLimitOrder) ValidateBasic() error { return nil }

// MsgCreateBinaryOptionsLimitOrder
func (msg *MsgCreateBinaryOptionsLimitOrder) GetSigners() []sdk.AccAddress {
	addr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{addr}
}

func (msg *MsgCreateBinaryOptionsLimitOrder) ValidateBasic() error { return nil }
