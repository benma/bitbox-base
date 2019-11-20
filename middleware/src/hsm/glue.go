package hsm

import (
	"log"

	"github.com/digitalbitbox/bitbox02-api-go/communication/usart"
	"github.com/flynn/noise"
)

// See ConfigInterace: https://github.com/digitalbitbox/bitbox02-api-go/blob/e8ae46debc009cfc7a64f45ec191de0220f0c401/api/firmware/device.go#L50
type bitbox02Config struct{}

func (bb02Config *bitbox02Config) ContainsDeviceStaticPubkey(pubkey []byte) bool {
	return false
}
func (bb02Config *bitbox02Config) AddDeviceStaticPubkey(pubkey []byte) error {
	return nil
}
func (bb02Config *bitbox02Config) GetAppNoiseStaticKeypair() *noise.DHKey {
	return nil
}
func (bb02Config *bitbox02Config) SetAppNoiseStaticKeypair(key *noise.DHKey) error {
	return nil
}

type bitbox02Logger struct{}

func (bb02Logger *bitbox02Logger) Error(msg string, err error) {
	log.Println(msg, err)
}
func (bb02Logger *bitbox02Logger) Info(msg string) {
	log.Println(msg)
}
func (bb02Logger *bitbox02Logger) Debug(msg string) {
	log.Println(msg)
}

// just translating SendFrame with incompatible signature (string<->[]byte), will be made consistent
// later...
type usartCommunication struct {
	*usart.Communication
}

func (communication usartCommunication) SendFrame(msg string) error {
	return communication.Communication.SendFrame([]byte(msg))
}
