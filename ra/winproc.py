import json
import msvcrt
import os
import subprocess

import win32api
import win32con
import win32event
import win32file
import win32job
import win32pipe
import win32process
import win32security
import win32ts

from ra.cmdrun import get_elevated_token


class ActiveUserProcess:
    def __init__(self, h_process, pid, stdin_w, job, null_out):
        self.h_process = h_process
        self.pid = pid
        self.stdin_w = stdin_w
        self.job = job
        self.null_out = null_out

    def write_json(self, payload: dict):
        if not self.stdin_w:
            return
        data = (json.dumps(payload, ensure_ascii=False) + "\n").encode("utf-8")
        win32file.WriteFile(self.stdin_w, data)

    def wait(self, timeout_ms: int) -> bool:
        rc = win32event.WaitForSingleObject(self.h_process, timeout_ms)
        return rc == win32con.WAIT_OBJECT_0

    def terminate(self, exit_code: int = 1):
        win32process.TerminateProcess(self.h_process, exit_code)

    def close(self):
        for handle in (self.stdin_w, self.h_process, self.job):
            if handle:
                try:
                    win32api.CloseHandle(handle)
                except Exception:
                    pass

        if self.null_out:
            try:
                self.null_out.close()
            except Exception:
                pass


def _create_kill_on_close_job():
    job = win32job.CreateJobObject(None, "")
    info = win32job.QueryInformationJobObject(
        job,
        win32job.JobObjectExtendedLimitInformation,
    )
    info["BasicLimitInformation"]["LimitFlags"] |= win32job.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    win32job.SetInformationJobObject(
        job,
        win32job.JobObjectExtendedLimitInformation,
        info,
    )
    return job


def spawn_hidden_as_active_user(args, cwd: str, env: dict) -> ActiveUserProcess:
    session_id = win32ts.WTSGetActiveConsoleSessionId()
    user_token = win32ts.WTSQueryUserToken(session_id)
    primary_token = get_elevated_token(user_token)

    sa = win32security.SECURITY_ATTRIBUTES()
    sa.bInheritHandle = True

    h_stdin_r, h_stdin_w = win32pipe.CreatePipe(sa, 0)
    win32api.SetHandleInformation(h_stdin_w, win32con.HANDLE_FLAG_INHERIT, 0)

    null_out = open(os.devnull, "w")
    h_null_out = msvcrt.get_osfhandle(null_out.fileno())

    startup = win32process.STARTUPINFO()
    startup.dwFlags |= win32con.STARTF_USESHOWWINDOW | win32con.STARTF_USESTDHANDLES
    startup.wShowWindow = win32con.SW_HIDE
    startup.lpDesktop = "winsta0\\default"
    startup.hStdInput = h_stdin_r
    startup.hStdOutput = h_null_out
    startup.hStdError = h_null_out

    creation_flags = win32con.CREATE_NEW_PROCESS_GROUP | win32con.CREATE_NO_WINDOW
    cmdline = subprocess.list2cmdline(args)

    h_process, h_thread, pid, _ = win32process.CreateProcessAsUser(
        primary_token,
        None,
        cmdline,
        None,
        None,
        True,
        creation_flags,
        env,
        cwd,
        startup,
    )

    win32api.CloseHandle(h_thread)
    win32api.CloseHandle(h_stdin_r)

    job = _create_kill_on_close_job()
    win32job.AssignProcessToJobObject(job, h_process)

    return ActiveUserProcess(
        h_process=h_process,
        pid=pid,
        stdin_w=h_stdin_w,
        job=job,
        null_out=null_out,
    )
