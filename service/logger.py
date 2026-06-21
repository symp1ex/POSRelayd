import about
import os
import sys
import logging
from logging.handlers import TimedRotatingFileHandler
from datetime import datetime

name_log_folder = "logs"
log_folder = os.path.join(about.current_path, name_log_folder)
level = "INFO"
log_days = 7


class StdoutRedirectHandler(logging.StreamHandler):
    def __init__(self):
        # Вызываем StreamHandler с sys.stdout, если он определен, иначе используем None
        super().__init__(stream=sys.stdout if hasattr(sys, 'stdout') else None)

    def emit(self, record):
        # Проверяем, что sys.stdout все еще доступен
        if hasattr(sys, 'stdout') and sys.stdout:
            # Форматируем сообщение перед выводом
            msg = self.format(record)
            # Пишем сообщение в sys.stdout (перехватывается виджетом)
            sys.stdout.write(msg + '\n')

def message_not_logger(message):
    try:
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        with open('logs\\logger.log', 'a', encoding='utf-8') as file:
            file.write(f'\n[{timestamp}] [ERROR] {message}.')
    except:
        pass


def logger(file_name, with_console=False):
    import service.configs
    global level, log_days

    try:
        # Словарь для маппинга строковых значений в константы logging
        LOG_LEVELS = {
            "DEBUG": logging.DEBUG,
            "INFO": logging.INFO,
            "WARNING": logging.WARNING,
            "ERROR": logging.ERROR,
            "CRITICAL": logging.CRITICAL
        }

        try:
            config_name = "service.json"
            config = service.configs.read_config_file(about.current_path, config_name, service.configs.service_data,
                                                      create=True)
        except:
            pass

        try: days = int(config.get("service", {}).get("log_days", 7))
        except Exception: days = 7

        if not os.path.exists(log_folder):
            os.makedirs(log_folder)

        try: log_level = config.get("service", {}).get('log_level', 'INFO').upper()  # INFO будет значением по умолчанию
        except: log_level = "INFO"

        if log_level not in LOG_LEVELS:
            log_level = "INFO"

        level = log_level
        log_days = days

        # Создаем логгер
        logger = logging.getLogger(file_name)
        logger.setLevel(LOG_LEVELS[log_level])

        # Проверяем, не был ли уже добавлен обработчик для этого логгера
        if not logger.hasHandlers():
            # Создаем обработчик для вывода в файл с ротацией
            file_handler = TimedRotatingFileHandler(
                f"{log_folder}\\{file_name}.log",
                when="midnight",         # Ротация в полночь
                interval=1,       # Интервал: 1 день
                backupCount=days,     # Хранить архивы не дольше 7 дней
                encoding="utf-8"
            )
            file_handler.setLevel(LOG_LEVELS[log_level])

            # Форматтер для настройки формата сообщений
            formatter = logging.Formatter('[%(asctime)s] [%(levelname)s] %(message)s')
            file_handler.setFormatter(formatter)

            # Добавляем обработчик к логгеру
            logger.addHandler(file_handler)

        # Проверяем, нужно ли создать новый файл лога
        current_date = datetime.now().date()
        log_file_path = f"{log_folder}/{file_name}.log"

        if os.path.exists(log_file_path):
            # Получаем дату последней модификации файла
            last_modified_date = datetime.fromtimestamp(os.path.getmtime(log_file_path)).date()
            if last_modified_date < current_date:
                # Если дата последней модификации меньше текущей, создаем новый файл
                file_handler.doRollover()

            # Добавляем обработчик для вывода на консоль
            if with_console:
                #console_handler = logging.StreamHandler() # вывод в стандартный обработчик бибилиотеки
                console_handler = StdoutRedirectHandler() # в системный вывод
                console_handler.setLevel(logging.INFO)
                console_handler.setFormatter(formatter)
                logger.addHandler(console_handler)

        return logger
    except Exception as e:
        message_not_logger(f"Не удалось сохранить запись в log-файл: {e}")

logger_service = logger(f"service", with_console=True)
kkt = logger(f"kkt", with_console=True)
