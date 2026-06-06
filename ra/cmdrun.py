import service.logger
import win32job
import win32ts
import win32process
import win32con
import win32security
import win32api
import win32pipe
import win32file
import subprocess
import threading
import codecs

OEM_ENCODING = "oem"

class CmdContextManager:
    def __init__(self):
        self.proc = None
        self.buffer = b""
        self.initial_output = None
        self.initial_output_captured = False
        self.alive = threading.Event()
        self.reader_thread = None
        self.job = None
        self.user_session = None

    def __enter__(self):
        self.job = win32job.CreateJobObject(None, "")

        try:
            session_id = win32ts.WTSGetActiveConsoleSessionId()
            service.logger.logger_service.debug(f"Id активной пользовательской сессии: {session_id}")

            user_token = win32ts.WTSQueryUserToken(session_id)
            service.logger.logger_service.debug(f"Пользовательский токен активной сессии: {user_token}")

            admin_token = get_elevated_token(user_token)
            service.logger.logger_service.debug(f"Администраторский токен активной сессии: {admin_token}")

            self.user_session = True
        except Exception:
            service.logger.logger_service.warning(
                f"Не удалось определить активную сессию пользователя, процесс 'cmd.exe' будет запущен от имени 'SYSTEM'",
                exc_info=True)

        info = win32job.QueryInformationJobObject(
            self.job,
            win32job.JobObjectExtendedLimitInformation
        )

        info["BasicLimitInformation"]["LimitFlags"] |= (
            win32job.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        )

        win32job.SetInformationJobObject(
            self.job,
            win32job.JobObjectExtendedLimitInformation,
            info
        )

        if self.user_session == True:
            proc = create_cmd_as_user(admin_token)
            self.proc = proc
            win32job.AssignProcessToJobObject(self.job, proc["hProcess"])
        else:
            self.proc = subprocess.Popen(
                ["cmd.exe"],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                bufsize=0,
                creationflags=subprocess.CREATE_NEW_PROCESS_GROUP
            )

            win32job.AssignProcessToJobObject(self.job, self.proc._handle)


        self.alive.set()
        self.reader_thread = threading.Thread(
            target=self._read_stdout,
            daemon=True
        )
        self.reader_thread.start()
        return self

    def _read_stdout(self):
        try:
            while self.alive.is_set():
                if self.user_session == True:
                    hr, data = win32file.ReadFile(self.proc["stdout"], 4096)
                else:
                    data = self.proc.stdout.read(4096)

                if not data:
                    break
                self.buffer += data

                if not self.initial_output_captured:
                    decoded = self.buffer.decode(OEM_ENCODING, errors="replace")

                    # cmd готов — появился prompt
                    if decoded.rstrip().endswith(">"):
                        self.initial_output = decoded
                        self.initial_output_captured = True
        except Exception:
            pass

    def write(self, text: str):
        if self.user_session == True:
            win32file.WriteFile(
                self.proc["stdin"],
                text.encode(OEM_ENCODING)
            )
        else:
            if self.proc and self.proc.stdin:
                self.proc.stdin.write(text.encode(OEM_ENCODING))
                self.proc.stdin.flush()

    def read(self) -> str:
        return self.buffer.decode(OEM_ENCODING, errors="replace")

    def clear(self):
        self.buffer = b""

    def __exit__(self, exc_type, exc, tb):
        self.alive.clear()
        try:
            if self.job:
                win32api.CloseHandle(self.job)
        except Exception:
            pass


def create_cmd_as_user(user_token):
    sa = win32security.SECURITY_ATTRIBUTES()
    sa.bInheritHandle = True

    # stdout / stderr
    hStdOutR, hStdOutW = win32pipe.CreatePipe(sa, 0)
    win32api.SetHandleInformation(
        hStdOutR,
        win32con.HANDLE_FLAG_INHERIT,
        0
    )

    # stdin
    hStdInR, hStdInW = win32pipe.CreatePipe(sa, 0)
    win32api.SetHandleInformation(
        hStdInW,
        win32con.HANDLE_FLAG_INHERIT,
        0
    )

    startup = win32process.STARTUPINFO()
    startup.dwFlags |= (
            win32con.STARTF_USESTDHANDLES |
            win32con.STARTF_USESHOWWINDOW
    )
    startup.wShowWindow = win32con.SW_HIDE

    startup.hStdInput = hStdInR
    startup.hStdOutput = hStdOutW
    startup.hStdError = hStdOutW

    creation_flags = (
            win32con.CREATE_NEW_PROCESS_GROUP |
            win32con.CREATE_NO_WINDOW
    )

    proc_info = win32process.CreateProcessAsUser(
        user_token,
        None,
        "cmd.exe",
        None,
        None,
        True,
        creation_flags,
        None,
        None,
        startup
    )

    # закрываем неиспользуемые хэндлы в родителе
    win32api.CloseHandle(hStdOutW)
    win32api.CloseHandle(hStdInR)

    return {
        "hProcess": proc_info[0],
        "pid": proc_info[2],
        "stdin": hStdInW,
        "stdout": hStdOutR
    }

def get_elevated_token(user_token):
    token_info = win32security.GetTokenInformation(
        user_token,
        win32security.TokenElevationType
    )

    if token_info == win32security.TokenElevationTypeLimited:
        return win32security.GetTokenInformation(
            user_token,
            win32security.TokenLinkedToken
        )

    return user_token

def is_process_alive(hProcess):
    code = win32process.GetExitCodeProcess(hProcess)
    return code == win32con.STILL_ACTIVE

def decode_cmd_bytes(data: bytes) -> str:
    if not data:
        return ""

    if data.startswith(codecs.BOM_UTF8):
        return data.decode("utf-8-sig", errors="replace")

    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return data.decode(OEM_ENCODING, errors="replace")

