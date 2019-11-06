package main

import (
	"fmt"
	"io"
	"log"

	"github.com/davecgh/go-spew/spew"
	"github.com/digitalbitbox/bitbox02-api-go/api/bootloader"
	"github.com/digitalbitbox/bitbox02-api-go/api/common"
	"github.com/digitalbitbox/bitbox02-api-go/api/firmware"
	"github.com/digitalbitbox/bitbox02-api-go/communication/u2fhid"
	"github.com/digitalbitbox/bitbox02-api-go/communication/usart"
	"github.com/digitalbitbox/bitbox02-api-go/util/semver"
	"github.com/flynn/noise"
	"github.com/karalabe/usb"
	"github.com/tarm/serial"
)

func getBitBox02USB() io.ReadWriteCloser {
	isBitBox02 := func(deviceInfo usb.DeviceInfo) bool {
		const (
			bitbox02VendorID  = 0x03eb
			bitbox02ProductID = 0x2403
		)

		return deviceInfo.VendorID == bitbox02VendorID &&
			deviceInfo.ProductID == bitbox02ProductID &&
			(deviceInfo.UsagePage == 0xffff || deviceInfo.Interface == 0)
	}

	deviceInfos, err := usb.EnumerateHid(0, 0)
	if err != nil {
		panic(err)
	}
	for _, deviceInfo := range deviceInfos {
		if isBitBox02(deviceInfo) {
			device, err := deviceInfo.Open()
			if err != nil {
				panic(err)
			}
			return device
		}
	}
	panic("no bb02 found")
}

func getBitBox02UART() io.ReadWriteCloser {
	c := &serial.Config{Name: "/dev/ttyUSB0", Baud: 115200}
	s, err := serial.OpenPort(c)
	if err != nil {
		panic(err)
	}
	return s
}

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

func hsmFirmwareTest(communication firmware.Communication) {

	device := firmware.NewDevice(
		// hardcoded version for now, in the future can be `nil` with autodetection using OP_INFO
		semver.NewSemVer(4, 2, 2),
		// Edition also hardcoded for now, will need to convert the stuff to platform/edition tuples
		common.EditionStandard,
		&bitbox02Config{},
		communication,
		&bitbox02Logger{},
	)

	err := device.Init()
	if err != nil {
		panic(err)
	}
	status := device.Status()
	switch status {
	case firmware.StatusUnpaired:
		// expected, proceed below.
	case firmware.StatusRequireAppUpgrade:
		panic("firmware unsupported, update the bitbox02 library")
	case firmware.StatusPairingFailed:
		panic("device was expected to autoconfirm the pairing")
	default:
		panic(fmt.Sprintf("unexpected status: %v ", status))
	}

	// autoconfirm pairing on the host
	device.ChannelHashVerify(true)

	status = device.Status()
	switch status {
	case firmware.StatusRequireFirmwareUpgrade:
		panic("have to upgrade firmware")
	case firmware.StatusInitialized, firmware.StatusUninitialized:
		fmt.Println("trying DeviceInfo()")
		info, err := device.DeviceInfo()
		if err != nil {
			panic(err)
		}
		spew.Dump(info)
	default:
		panic(fmt.Sprintf("unexpected status: %v ", status))
	}
}

func hsmBootloaderTest(communication bootloader.Communication) {
	device := bootloader.NewDevice(
		// hardcoded version for now, in the future can be `nil` with autodetection using OP_INFO
		semver.NewSemVer(1, 0, 1),
		// Edition also hardcoded for now, will need to convert the stuff to platform/edition tuples
		common.EditionStandard,
		communication,
		func(status *bootloader.Status) {
			fmt.Println("status changed:", status)
		},
	)
	firmwareHash, _, err := device.GetHashes(false, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("firmware hash returned by bootloader: %x\n", firmwareHash)
}

func hsmTest() {

	const firmwareCMD = 0x80 + 0x40 + 0x01
	const bootloaderCMD = 0x80 + 0x40 + 0x03

	const cmd = firmwareCMD
	// open device (io.ReadWriteCloser interface with Read, Write, Close functions).
	// a) can be u2fhid/USB
	deviceUSB := getBitBox02USB()
	communication := u2fhid.NewCommunication(deviceUSB, cmd)
	// b) can be usart/Serial:
	// deviceUART := getBitBox02UART()
	// endpoint := byte(0x00) // will be deleted from the spec
	// communication := usartCommunication{usart.NewCommunication(deviceUART, cmd, endpoint)}

	hsmFirmwareTest(communication)

	// make sure to use bootloaderCMD above
	//hsmBootloaderTest(communication)
}
