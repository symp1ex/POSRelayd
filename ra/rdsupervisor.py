import json
import os
import subprocess
import threading
import time
import service.crypto
import about
import service.logger
import service.sys_manager

sys_manager = service.sys_manager.ResourceManagement()
client_identity = service.crypto.ClientIdentityManager()


class RDAgentSupervisor:
    def __init__(self, ws_url: str, client_id: str, api_key: str):
        self.ws_url = ws_url
        self.client_id = client_id
        self.api_key = api_key
        self.rd_agents = {}  # session_id -> subprocess.Popen
        self._lock = threading.RLock()

        self.base_dir = about.current_path
        self.agent_name = "rd-agent.exe"
        self.agent_path = os.path.join(self.base_dir, sys_manager.resource_path, "bin", self.agent_name)

        self.work_dir = os.path.join(self.base_dir, sys_manager.resource_path, "bin")
        if not os.path.exists(self.work_dir):
            os.makedirs(self.work_dir)

        self.log_dir = os.path.abspath(service.logger.log_folder)
        self.log_level = service.logger.level
        self.log_days = service.logger.log_days

    def start(self, session_id: str, token: str, turn_config):
        if not session_id:
            service.logger.logger_service.warning("rd_start без session_id")
            return False

        if not token:
            service.logger.logger_service.warning(
                f"rd_start без token для session_id='{session_id}'"
            )
            return False

        if not self.api_key:
            service.logger.logger_service.warning(
                f"rd-agent не будет запущен: пустой api_key для session_id='{session_id}'"
            )
            return False

        with self._lock:
            old_proc = self.rd_agents.get(session_id)
            if old_proc and old_proc.poll() is None:
                service.logger.logger_service.warning(
                    f"rd-agent уже запущен для session_id='{session_id}', старый процесс будет остановлен"
                )
                self.stop(session_id)

            if not os.path.exists(self.agent_path):
                service.logger.logger_service.error(
                    f"rd-agent.exe не найден: '{self.agent_path}'"
                )
                return False

            env = os.environ.copy()
            env["RD_WS_URL"] = self.ws_url
            env["RD_CLIENT_ID"] = self.client_id
            env["RD_SESSION_ID"] = session_id
            env["RD_TOKEN"] = token
            env["RD_TURN_JSON"] = json.dumps(turn_config or {}, ensure_ascii=False)
            env["RD_LOG_FILE"] = self.log_dir
            env["RD_WORK_DIR"] = self.work_dir

            args = [
                self.agent_path,
                "--ws-url", self.ws_url,
                "--api-key", self.api_key,
                "--client-id", self.client_id,
                "--session-id", session_id,
                "--token", token,
                "--private-key-file", client_identity.PRIVATE_KEY_PATH,
                "--log-dir", self.log_dir,
                "--log-level", self.log_level,
                "--log-retain-days", str(self.log_days),
            ]

            try:
                creationflags = 0
                if hasattr(subprocess, "CREATE_NEW_PROCESS_GROUP"):
                    creationflags |= subprocess.CREATE_NEW_PROCESS_GROUP
                if hasattr(subprocess, "CREATE_NO_WINDOW"):
                    creationflags |= subprocess.CREATE_NO_WINDOW

                proc = subprocess.Popen(
                    args,
                    cwd=self.work_dir,
                    env=env,
                    stdin=subprocess.PIPE,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    shell=False,
                    creationflags=creationflags,
                    text=True,
                    encoding="utf-8",
                    errors="replace",
                )

                self.rd_agents[session_id] = proc

                service.logger.logger_service.info(
                    f"Запущен rd-agent для session_id='{session_id}', pid={proc.pid}"
                )
                service.logger.logger_service.debug(
                    f"rd-agent log_path='{self.log_dir}', work_dir='{self.work_dir}'"
                )
                return True

            except Exception:
                service.logger.logger_service.error(
                    f"Не удалось запустить rd-agent для session_id='{session_id}'",
                    exc_info=True
                )
                self.rd_agents.pop(session_id, None)
                return False

    def forward_signal(self, session_id: str, message: dict):
        if not session_id:
            service.logger.logger_service.warning(
                f"RD signaling без session_id: '{message}'"
            )
            return False

        with self._lock:
            proc = self.rd_agents.get(session_id)

            if not proc or proc.poll() is not None:
                service.logger.logger_service.warning(
                    f"Получено RD signaling, но rd-agent не активен для session_id='{session_id}'"
                )
                self.rd_agents.pop(session_id, None)
                return False

            if not proc.stdin:
                service.logger.logger_service.warning(
                    f"stdin недоступен у rd-agent для session_id='{session_id}'"
                )
                return False

            try:
                proc.stdin.write(json.dumps(message, ensure_ascii=False) + "\n")
                proc.stdin.flush()
                return True
            except Exception:
                service.logger.logger_service.error(
                    f"Не удалось передать RD signaling в rd-agent для session_id='{session_id}'",
                    exc_info=True
                )
                self.stop(session_id)
                return False

    def stop(self, session_id: str, timeout: float = 5.0):
        with self._lock:
            proc = self.rd_agents.pop(session_id, None)

        if not proc:
            return

        try:
            if proc.poll() is not None:
                service.logger.logger_service.debug(
                    f"rd-agent уже завершён для session_id='{session_id}'"
                )
                return

            service.logger.logger_service.info(
                f"Остановка rd-agent для session_id='{session_id}', pid={proc.pid}"
            )

            try:
                if proc.stdin:
                    proc.stdin.write(json.dumps({
                        "type": "rd_shutdown",
                        "id": session_id
                    }) + "\n")
                    proc.stdin.flush()
            except Exception:
                pass

            proc.terminate()

            deadline = time.time() + timeout
            while proc.poll() is None and time.time() < deadline:
                time.sleep(0.1)

            if proc.poll() is None:
                service.logger.logger_service.warning(
                    f"rd-agent не завершился штатно, будет принудительно остановлен: session_id='{session_id}', pid={proc.pid}"
                )
                proc.kill()
                proc.wait(timeout=2)

        except Exception:
            service.logger.logger_service.error(
                f"Ошибка при остановке rd-agent для session_id='{session_id}'",
                exc_info=True
            )

    def stop_all(self):
        with self._lock:
            session_ids = list(self.rd_agents.keys())

        for session_id in session_ids:
            self.stop(session_id)