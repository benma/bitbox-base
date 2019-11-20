package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/digitalbitbox/bitbox-base/middleware/src/hsm"
	bb02bootloader "github.com/digitalbitbox/bitbox02-api-go/api/bootloader"
	"github.com/digitalbitbox/bitbox02-api-go/api/common"
	"github.com/digitalbitbox/bitbox02-api-go/api/firmware"
	bb02firmware "github.com/digitalbitbox/bitbox02-api-go/api/firmware"
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
	s, err := serial.OpenPort(&serial.Config{
		Name:        "/dev/ttyUSB0",
		Baud:        115200,
		ReadTimeout: 5 * time.Second,
	})
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

func hsmFirmwareTest(communication bb02firmware.Communication) {
	b := common.ProductBitBoxBaseStandard
	device := bb02firmware.NewDevice(
		// version and product infered via OP_INFO
		semver.NewSemVer(4, 3, 0), &b,
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
	case bb02firmware.StatusUnpaired:
		// expected, proceed below.
	case bb02firmware.StatusRequireAppUpgrade:
		panic("firmware unsupported, update the bitbox02 library")
	case bb02firmware.StatusPairingFailed:
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
	case firmware.StatusUninitialized:
		device.UpgradeFirmware()
	default:
		panic(fmt.Sprintf("unexpected status: %v ", status))
	}
}

func hsmBootloaderTest(communication bb02bootloader.Communication) {
	device := bb02bootloader.NewDevice(
		// hardcoded version for now, in the future can be `nil` with autodetection using OP_INFO
		semver.NewSemVer(1, 0, 1),
		common.ProductBitBoxBaseStandard,
		communication,
		func(status *bb02bootloader.Status) {
			spew.Dump("status changed:", status)
		},
	)
	firmwareHash, _, err := device.GetHashes(false, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("firmware hash returned by bootloader: %x\n", firmwareHash)
	//device.Reboot()
	firmwareBinary, err := ioutil.ReadFile("/home/marko/coding/dbb/bitbox02-firmware/build/bin/firmware-bitboxbase.bin")
	if err != nil {
		panic(err)
	}
	err = device.UpgradeFirmware(firmwareBinary)
	if err != nil {
		panic(err)
	}
}

func hsmTest() {
	device := hsm.NewHSM("/dev/ttyUSB0")
	if err := device.InteractWithBootloader(func(bootloader *bb02bootloader.Device) {
		fmt.Println("OK")
	}); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	if err := device.InteractWithFirmware(func(firmware *bb02firmware.Device) {
		fmt.Println("OK firmware")
	}); err != nil {
		panic(err)
	}

	return
	const firmwareCMD = 0x80 + 0x40 + 0x01
	const bootloaderCMD = 0x80 + 0x40 + 0x03

	const cmd = firmwareCMD
	//const cmd = bootloaderCMD
	// open device (io.ReadWriteCloser interface with Read, Write, Close functions).
	// a) can be u2fhid/USB
	// deviceUSB := getBitBox02USB()
	// communication := u2fhid.NewCommunication(deviceUSB, cmd)
	// b) can be usart/Serial:
	deviceUART := getBitBox02UART()
	communication := usartCommunication{usart.NewCommunication(deviceUART, cmd)}

	hsmFirmwareTest(communication)

	// make sure to use bootloaderCMD above
	//hsmBootloaderTest(communication)
}
