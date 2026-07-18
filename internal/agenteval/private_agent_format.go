package agenteval

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"runtime"
)

type privateAgentNativeFormat string

const (
	privateAgentFormatUnknown privateAgentNativeFormat = ""
	privateAgentFormatELF     privateAgentNativeFormat = "elf"
	privateAgentFormatMachO   privateAgentNativeFormat = "macho"
	privateAgentFormatPE      privateAgentNativeFormat = "pe"
)

func validatePrivateAgentNativeFormat(data []byte) error {
	format := detectPrivateAgentNativeFormat(data)
	want := privateAgentFormatELF
	switch runtime.GOOS {
	case "darwin":
		want = privateAgentFormatMachO
	case "windows":
		want = privateAgentFormatPE
	case "linux":
	default:
		return privatePlanError("agent_binary_platform")
	}
	if format == privateAgentFormatUnknown {
		return privatePlanError("agent_binary_format")
	}
	if format != want {
		return privatePlanError("agent_binary_platform")
	}
	var err error
	switch format {
	case privateAgentFormatELF:
		err = validatePrivateAgentELF(data)
	case privateAgentFormatMachO:
		err = validatePrivateAgentMachO(data)
	case privateAgentFormatPE:
		err = validatePrivateAgentPE(data)
	}
	return err
}

func detectPrivateAgentNativeFormat(data []byte) privateAgentNativeFormat {
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return privateAgentFormatELF
	}
	if len(data) >= 2 && data[0] == 'M' && data[1] == 'Z' {
		return privateAgentFormatPE
	}
	if len(data) >= 4 {
		for _, magic := range [][]byte{
			{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe},
			{0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe},
			{0xca, 0xfe, 0xba, 0xbe},
		} {
			if bytes.Equal(data[:4], magic) {
				return privateAgentFormatMachO
			}
		}
	}
	return privateAgentFormatUnknown
}

func validatePrivateAgentELF(data []byte) error {
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) || file.Entry == 0 {
		return privatePlanError("agent_binary_format")
	}
	if file.Machine != privateAgentELFMachine(runtime.GOARCH) {
		return privatePlanError("agent_binary_platform")
	}
	return nil
}

func privateAgentELFMachine(arch string) elf.Machine {
	switch arch {
	case "386":
		return elf.EM_386
	case "amd64":
		return elf.EM_X86_64
	case "arm":
		return elf.EM_ARM
	case "arm64":
		return elf.EM_AARCH64
	case "ppc64", "ppc64le":
		return elf.EM_PPC64
	case "riscv64":
		return elf.EM_RISCV
	case "s390x":
		return elf.EM_S390
	default:
		return elf.EM_NONE
	}
}

func validatePrivateAgentMachO(data []byte) error {
	reader := bytes.NewReader(data)
	if fat, err := macho.NewFatFile(reader); err == nil {
		hostArchitecture := false
		for _, arch := range fat.Arches {
			if arch.Cpu == privateAgentMachOCPU(runtime.GOARCH) {
				hostArchitecture = true
			}
			if validPrivateAgentMachOExecutable(arch.File, runtime.GOARCH) {
				return nil
			}
		}
		if hostArchitecture {
			return privatePlanError("agent_binary_format")
		}
		return privatePlanError("agent_binary_platform")
	}
	file, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return privatePlanError("agent_binary_format")
	}
	if file.Type != macho.TypeExec || file.Cpu != privateAgentMachOCPU(runtime.GOARCH) {
		return privatePlanError("agent_binary_platform")
	}
	if !validPrivateAgentMachOExecutable(file, runtime.GOARCH) {
		return privatePlanError("agent_binary_format")
	}
	return nil
}

const privateAgentMachOLoadMain macho.LoadCmd = 0x80000028

func validPrivateAgentMachOExecutable(file *macho.File, arch string) bool {
	if file == nil || file.Type != macho.TypeExec || file.Cpu != privateAgentMachOCPU(arch) || file.ByteOrder == nil {
		return false
	}
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) >= 24 && macho.LoadCmd(file.ByteOrder.Uint32(raw[:4])) == privateAgentMachOLoadMain && file.ByteOrder.Uint64(raw[8:16]) != 0 {
			return true
		}
	}
	return false
}

func privateAgentMachOCPU(arch string) macho.Cpu {
	switch arch {
	case "386":
		return macho.Cpu386
	case "amd64":
		return macho.CpuAmd64
	case "arm":
		return macho.CpuArm
	case "arm64":
		return macho.CpuArm64
	default:
		return 0
	}
}

func validatePrivateAgentPE(data []byte) error {
	file, err := pe.NewFile(bytes.NewReader(data))
	if err != nil || file.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
		return privatePlanError("agent_binary_format")
	}
	if file.Machine != privateAgentPEMachine(runtime.GOARCH) {
		return privatePlanError("agent_binary_platform")
	}
	switch optional := file.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		if optional.AddressOfEntryPoint != 0 {
			return nil
		}
	case *pe.OptionalHeader64:
		if optional.AddressOfEntryPoint != 0 {
			return nil
		}
	default:
	}
	return privatePlanError("agent_binary_format")
}

func privateAgentPEMachine(arch string) uint16 {
	switch arch {
	case "386":
		return pe.IMAGE_FILE_MACHINE_I386
	case "amd64":
		return pe.IMAGE_FILE_MACHINE_AMD64
	case "arm":
		return pe.IMAGE_FILE_MACHINE_ARMNT
	case "arm64":
		return pe.IMAGE_FILE_MACHINE_ARM64
	default:
		return 0
	}
}
