package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// job é um Job Object do Windows com KILL_ON_JOB_CLOSE: se o agente morrer de
// qualquer jeito, o Windows encerra a API junto. Sem isso uma API órfã poderia
// continuar segurando a porta com a versão antiga.
type job struct{ h windows.Handle }

func novoJob() (job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return job{}, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(h)
		return job{}, err
	}
	return job{h: h}, nil
}

func (j job) anexar(p *os.Process) error {
	if j.h == 0 {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(j.h, h)
}

func configurarProcesso(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
