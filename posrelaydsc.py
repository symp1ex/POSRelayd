import service.logger
import service.configs
import service.sys_manager
import service.fn_check
import service.connectors
import getdata.atol.atol
import getdata.shtrih
import getdata.mitsu
import about
import win32serviceutil
import win32service
import win32event
import servicemanager
import socket
import sys
import os
import time
import multiprocessing
import threading

validation_fn = service.fn_check.ValidationFn()
shtrihscanner = getdata.shtrih.ShtrihData()
mitsu = getdata.mitsu.MitsuGetData()
sending_data = service.connectors.SendingData()


def run_without_arguments():
    try:
        service.logger.logger_service.info("Произведён запуск исполняемого файла не от имени службы")

        getdata.atol.atol.get_atol_data()
        mitsu.get_data()

        if shtrihscanner.enabled == 1:
            shtrihscanner.subprocess_run("", shtrihscanner.exe_name)
            shc_procces_flag = 1

            for attempt in range(18):
                process_found = shtrihscanner.check_process(shtrihscanner.exe_name)
                if process_found:
                    service.logger.logger_service.debug("Следующая проверка через (5) секунд.")
                    time.sleep(5)
                    continue
                else:
                    service.logger.logger_service.info(
                        f"Процесс '{shtrihscanner.exe_name}' завершил свою работу или не был запущен")
                    shc_procces_flag = 0
                    break
            if shc_procces_flag:
                service.logger.logger_service.warn(
                    f"Процесс '{shtrihscanner.exe_name}' остаётся активным в течении (90) секунд, "
                    f"работа службы будет продолжена")
                shtrihscanner.subprocess_kill("", shtrihscanner.exe_name)

        if sending_data.sending_data_enabled == True:
            service.logger.logger_service.info("Производится отправка данных на сервер")
            sending_data.send_fiscals_data()

        if sending_data.updater_enabled == True:
            process_not_found = validation_fn.check_process_cycle(validation_fn.updater_name, count_attempt=120)
            if process_not_found:
                validation_fn.subprocess_run("updater", validation_fn.updater_name)
    except Exception:
        service.logger.logger_service.error(
            "Запуск исполняемого файла без аргументов завершился c ошибкой", exc_info=True)

def checking_launch_arguments():
    if "--pass" in sys.argv:
        validation_fn.clean_uuid()

        try:
            idx = sys.argv.index("--pass")
            password = sys.argv[idx + 1]
        except (IndexError, ValueError):
            service.logger.logger_service.warning("Usage: posrelaydsc.exe --pass <password>")
            sys.exit(1)

        from ra.setpass import send_password_once
        send_password_once(password)
        sys.exit(0)

    noip_url = None
    noip_key = None

    try:
        if "--noip-url" in sys.argv:
            idx = sys.argv.index("--noip-url")
            noip_url = sys.argv[idx + 1]

        if "--noip-key" in sys.argv:
            idx = sys.argv.index("--noip-key")
            noip_key = sys.argv[idx + 1]

        if noip_url or noip_key:
            validation_fn.save_noip_config(
                noip_url=noip_url,
                noip_key=noip_key
            )
            os._exit(0)
    except (IndexError, ValueError):
        service.logger.logger_service.warning.updater.critical(
            f"Usage:\n"
            f"posrelaydsc.exe --noip-url <url> OR\n"
            f"posrelaydsc.exe --noip-key <apikey> OR\n"
        )
        sys.exit(1)

    bot_token = None
    chat_id = None

    try:
        if "--bot-token" in sys.argv:
            idx = sys.argv.index("--bot-token")
            bot_token = sys.argv[idx + 1]

        if "--chat-id" in sys.argv:
            idx = sys.argv.index("--chat-id")
            chat_id = sys.argv[idx + 1]

        if bot_token or chat_id:
            validation_fn.save_telegram_config(
                bot_token=bot_token,
                chat_id=chat_id
            )
            os._exit(0)
    except (IndexError, ValueError):
        service.logger.logger_service.warning.updater.critical(
            f"Usage:\n"
            f"posrelaydsc.exe --bot-token <bot token> OR\n"
            f"posrelaydsc.exe --chat-id <chat id> OR\n"
        )
        sys.exit(1)

    db_url = None
    db_apikey = None

    try:
        if "--db-url" in sys.argv:
            idx = sys.argv.index("--db-url")
            db_url = sys.argv[idx + 1]

        if "--db-apikey" in sys.argv:
            idx = sys.argv.index("--db-apikey")
            db_apikey = sys.argv[idx + 1]

        if db_url or db_apikey:
            validation_fn.add_sending_data_endpoint(
                db_url=db_url,
                db_apikey=db_apikey
            )
            os._exit(0)
    except (IndexError, ValueError):
        service.logger.logger_service.warning.updater.critical(
            f"Usage only:\n"
            f"posrelaydsc.exe --db-url <db url> --db-apikey <db apikey>"
        )
        sys.exit(1)

def get_fiscals_data():
    getdata.atol.atol.get_atol_data()
    mitsu.get_data()

class Service(win32serviceutil.ServiceFramework):
    _svc_name_ = about.service_name  # Название службы
    _svc_display_name_ = about.service_name  # Отображаемое имя службы
    _svc_description_ = "Remote Access and Check Fiscal Service"  # Описание службы
    _svc_start_type_ = win32service.SERVICE_AUTO_START  # Автозапуск

    def __init__(self, args):
        win32serviceutil.ServiceFramework.__init__(self, args)
        self.hWaitStop = win32event.CreateEvent(None, 0, 0, None)
        socket.setdefaulttimeout(60)
        self.is_running = True
        self.cmdclient_instance = None

    def cmdclient_init(self):
        import ra.cmdroute
        self.cmdclient_instance = ra.cmdroute.CMDClient()

    def SvcStop(self):
        shtrihscanner.subprocess_kill("", shtrihscanner.exe_name)
        service.logger.logger_service.debug(f"Cписок активных cmd-сессий: {self.cmdclient_instance.sessions}")

        sessions = self.cmdclient_instance.sessions

        for admin_id in list(sessions):
            session = self.cmdclient_instance.sessions.pop(admin_id, None)
            if session:
                session.__exit__(None, None, None)

        self.ReportServiceStatus(win32service.SERVICE_STOP_PENDING)
        win32event.SetEvent(self.hWaitStop)
        self.is_running = False

        service_file = os.path.basename(sys.argv[0])
        service.logger.logger_service.debug(f"Исполняемый файл службы: '{service_file}'")
        validation_fn.subprocess_kill("", service_file)
        service.logger.logger_service.info("Служба остановлена")

    def SvcDoRun(self):
        servicemanager.LogMsg(
            servicemanager.EVENTLOG_INFORMATION_TYPE,
            servicemanager.PYS_SERVICE_STARTED,
            (self._svc_name_, '')
        )
        service.logger.logger_service.info("Служба запущена")
        service.logger.logger_service.info(f"Версия исполняемого файла: {about.version}")
        service_path = os.path.dirname(os.path.abspath(__file__))
        service.logger.logger_service.debug(f"Рабочая директория: '{service_path}'")
        self.main()

    def main(self):
        validation_fn.clean_uuid()

        try:
            self.cmdclient_init()

            if self.cmdclient_instance.ra_enabled == True and validation_fn.validation == 1:
                threading.Thread(target=self.cmdclient_instance.run, args=(self,), daemon=True).start()

            fiscals_data = multiprocessing.Process(target=get_fiscals_data)
            fiscals_data.daemon = True
            fiscals_data.start()
            fiscals_data.join()

            if shtrihscanner.enabled == 1:
                shtrihscanner.run(self)

            if validation_fn.validation == 1:
                validation_fn.fn_check_process(self)
            else:
                if sending_data.sending_data_enabled == True:
                    service.logger.logger_service.info("Производится отправка данных на сервер")
                    sending_data.send_fiscals_data()

                if self.cmdclient_instance.updater_enabled == True:
                    process_not_found = validation_fn.check_process_cycle(validation_fn.updater_name, kill_process=True)
                    if process_not_found:
                        validation_fn.subprocess_run("updater", validation_fn.updater_name)

                if self.cmdclient_instance.ra_enabled == True:
                    self.cmdclient_instance.run(self)
        except Exception:
            service.logger.logger_service.critical(
                f"Запуск основного потока службы завершился с ошибкой", exc_info=True)
            self.SvcStop()
            os._exit(1)


if __name__ == '__main__':
    multiprocessing.freeze_support()

    checking_launch_arguments()

    if len(sys.argv) == 1:
        try:
            servicemanager.Initialize()
            servicemanager.PrepareToHostSingle(Service)
            servicemanager.StartServiceCtrlDispatcher()
        except:
            run_without_arguments()
    else:
        win32serviceutil.HandleCommandLine(Service)
