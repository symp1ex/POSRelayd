import json
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

import service.logger


class WinUserProcess:
    def __init__(self, proc_info, stdin_w=None, stdout_r=None, stderr_r=None, job=None, popen=None):
        self.proc_info = proc_info
        self.stdin_w = stdin_w
        self.stdout_r = stdout_r
        self.stderr_r = stderr_r
        self.job = job
        self.popen = popen

        if popen is not None:
            self.pid = popen.pid
        else:
            self.hProcess = proc_info[0]
            self.hThread = proc_info[1]
            self.pid = proc_info[2]
            self.tid = proc_info[3]

    def poll(self):
        if self.popen is not None:
            return self.popen.poll()

        code = win32process.GetExitCodeProcess(self.hProcess)
        if code == win32con.STILL_ACTIVE:
            return None
        return code

    def wait(self, timeout_ms=None):
        if self.popen is not None:
            try:
                timeout = None if timeout_ms is None else timeout_ms / 1000
                self.popen.wait(timeout=timeout)
                return True
            except subprocess.TimeoutExpired:
                return False

        timeout = win32event.INFINITE if timeout_ms is None else int(timeout_ms)
        rc = win32event.WaitForSingleObject(self.hProcess, timeout)
        return rc == win32event.WAIT_OBJECT_0

    def terminate(self):
        if self.popen is not None:
            self.popen.terminate()
            return

        try:
            win32process.TerminateProcess(self.hProcess, 1)
        except Exception:
            service.logger.logger_service.warning(
                f"Не удалось завершить процесс pid={self.pid}",
                exc_info=True,
            )

    def write_json(self, obj):
        data = (json.dumps(obj, ensure_ascii=False) + "\n").encode("utf-8")

        if self.popen is not None:
            if self.popen.stdin:
                self.popen.stdin.write(data)
                self.popen.stdin.flush()
            return

        if self.stdin_w:
            win32file.WriteFile(self.stdin_w, data)

    def close(self):
        handles = [
            getattr(self, "stdin_w", None),
            getattr(self, "stdout_r", None),
            getattr(self, "stderr_r", None),
            getattr(self, "job", None),
            getattr(self, "hThread", None),
            getattr(self, "hProcess", None),
        ]

        for h in handles:
            if h:
                try:
                    win32api.CloseHandle(h)
                except Exception:
                    pass


def get_elevated_token(user_token):
    token_info = win32security.GetTokenInformation(
        user_token,
        win32security.TokenElevationType,
    )

    if token_info == win32security.TokenElevationTypeLimited:
        return win32security.GetTokenInformation(
            user_token,
            win32security.TokenLinkedToken,
        )

    return user_token


def create_job_kill_on_close():
    job = win32job.CreateJobObject(None, "")

    info = win32job.QueryInformationJobObject(
        job,
        win32job.JobObjectExtendedLimitInformation,
    )

    info["BasicLimitInformation"]["LimitFlags"] |= (
        win32job.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    )

    win32job.SetInformationJobObject(
        job,
        win32job.JobObjectExtendedLimitInformation,
        info,
    )

    return job


def quote_cmdline(args):
    return subprocess.list2cmdline(args)


def spawn_elevated_as_active_user(args, cwd=None, env=None, hidden=True, with_pipes=True):
    """
    Запускает процесс так же, как cmd-сессия:
    active console session -> WTSQueryUserToken -> linked elevated token -> CreateProcessAsUser.
    """

    session_id = win32ts.WTSGetActiveConsoleSessionId()
    service.logger.logger_service.debug(f"Id активной пользовательской сессии: {session_id}")

    user_token = win32ts.WTSQueryUserToken(session_id)
    service.logger.logger_service.debug(f"Пользовательский токен активной сессии: {user_token}")

    admin_token = get_elevated_token(user_token)
    service.logger.logger_service.debug(f"Администраторский токен активной сессии: {admin_token}")

    sa = win32security.SECURITY_ATTRIBUTES()
    sa.bInheritHandle = True

    stdin_r = stdin_w = None
    stdout_r = stdout_w = None
    stderr_r = stderr_w = None

    startup = win32process.STARTUPINFO()

    inherit_handles = False

    if hidden:
        startup.dwFlags |= win32con.STARTF_USESHOWWINDOW
        startup.wShowWindow = win32con.SW_HIDE

    if with_pipes:
        inherit_handles = True

        stdin_r, stdin_w = win32pipe.CreatePipe(sa, 0)
        win32api.SetHandleInformation(
            stdin_w,
            win32con.HANDLE_FLAG_INHERIT,
            0,
        )

        stdout_r, stdout_w = win32pipe.CreatePipe(sa, 0)
        win32api.SetHandleInformation(
            stdout_r,
            win32con.HANDLE_FLAG_INHERIT,
            0,
        )

        stderr_r, stderr_w = win32pipe.CreatePipe(sa, 0)
        win32api.SetHandleInformation(
            stderr_r,
            win32con.HANDLE_FLAG_INHERIT,
            0,
        )

        startup.dwFlags |= win32con.STARTF_USESTDHANDLES
        startup.hStdInput = stdin_r
        startup.hStdOutput = stdout_w
        startup.hStdError = stderr_w

    creation_flags = win32con.CREATE_NEW_PROCESS_GROUP

    if hidden:
        creation_flags |= win32con.CREATE_NO_WINDOW

    env_block = None
    if env is not None:
        env_block = env

    cmdline = quote_cmdline(args)

    job = create_job_kill_on_close()

    proc_info = win32process.CreateProcessAsUser(
        admin_token,
        None,
        cmdline,
        None,
        None,
        inherit_handles,
        creation_flags,
        env_block,
        cwd,
        startup,
    )

    try:
        win32job.AssignProcessToJobObject(job, proc_info[0])
    except Exception:
        service.logger.logger_service.warning(
            f"Не удалось добавить процесс в job object: pid={proc_info[2]}",
            exc_info=True,
        )

    for h in (stdin_r, stdout_w, stderr_w):
        if h:
            try:
                win32api.CloseHandle(h)
            except Exception:
                pass

    return WinUserProcess(
        proc_info=proc_info,
        stdin_w=stdin_w,
        stdout_r=stdout_r,
        stderr_r=stderr_r,
        job=job,
    )


def spawn_hidden_as_active_user(args, cwd=None, env=None):
    """
    Совместимое имя для текущего RDAgentSupervisor.
    Теперь использует тот же elevated active-user запуск, что и cmd.
    """
    try:
        return spawn_elevated_as_active_user(
            args=args,
            cwd=cwd,
            env=env,
            hidden=True,
            with_pipes=True,
        )
    except Exception:
        service.logger.logger_service.warning(
            "Не удалось запустить процесс в активной elevated-сессии, fallback на subprocess.Popen",
            exc_info=True,
        )

        creationflags = 0
        if hasattr(subprocess, "CREATE_NEW_PROCESS_GROUP"):
            creationflags |= subprocess.CREATE_NEW_PROCESS_GROUP
        if hasattr(subprocess, "CREATE_NO_WINDOW"):
            creationflags |= subprocess.CREATE_NO_WINDOW

        popen = subprocess.Popen(
            args,
            cwd=cwd,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            creationflags=creationflags,
        )

        return WinUserProcess(proc_info=None, popen=popen)