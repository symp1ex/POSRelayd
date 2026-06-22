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
        self.agent_name = "prrd.exe"
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

                from ra.winproc import spawn_hidden_as_local_system_active_session

                proc = spawn_hidden_as_local_system_active_session(
                    args=args,
                    cwd=self.work_dir,
                    env=env,
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

            if not proc:
                service.logger.logger_service.warning(
                    f"Получено RD signaling, но rd-agent не активен для session_id='{session_id}'"
                )
                return False

            try:
                proc.write_json(message)
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
            service.logger.logger_service.info(
                f"Остановка rd-agent для session_id='{session_id}', pid={proc.pid}"
            )

            try:
                proc.write_json({
                    "type": "rd_shutdown",
                    "id": session_id,
                    "session_id": session_id,
                })
            except Exception:
                pass

            if not proc.wait(int(timeout * 1000)):
                service.logger.logger_service.warning(
                    f"rd-agent не завершился штатно, будет принудительно остановлен: session_id='{session_id}', pid={proc.pid}"
                )
                proc.terminate()

            proc.close()

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