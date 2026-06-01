import service.logger
import about
import json
import os

connect_data = {
    "tcp_timeout": 30,
    "atol": [
        {
            "type_connect": 0,
            "com_port": "COM4",
            "com_baudrate": "115200",
            "ip": "192.168.0.90",
            "ip_port": "5555",
        },
        {
            "type_connect": 0,
            "com_port": "COM10",
            "com_baudrate": "115200",
            "ip": "192.168.0.91",
            "ip_port": "5555"
        }
    ],
    "mitsu": [
        {
            "type_connect": 0,
            "com_port": "COM14",
            "com_baudrate": "115200",
            "ip": "192.168.0.100",
            "ip_port": "8200"
        },
        {
            "type_connect": 0,
            "com_port": "COM20",
            "com_baudrate": "115200",
            "ip": "192.168.0.101",
            "ip_port": "8200"
        }
    ]
}

service_data = {
    "service": {
        "noip_connection": {
            "encryption": False,
            "url": "ws://10.127.33.42:22233/ws",
            "api_key": ""
        },
        "updater": {
            "enabled": True,
            "file_name": "updater.exe"
        },
        "log_level": "info",
        "log_days": 7
    },
    "sending_data": {
        "enabled": False,
        "url_list": [
            {
                "encryption": False,
                "url": "https://server.com/api/submit_json",
                "api_key": ""
            }
        ],
        "max_attempts": 5,
        "delay": 10
    },
    "validation_fn": {
        "enabled": True,
        "forced": False,
        "reboot_file": "reboot.bat",
        "interval": 8,
        "trigger_days": 2,
        "target_time": "05:30",
        "delete_days": 25,
        "atol": {
            "logs_dir": "iiko",
            "logs_mask_name": "AtolFiscalRegister",
            "serialNumber_key": "serialNumber=",
            "fnNumber_key": "fnNumber="
        },
        "mitsu": {
            "logs_dir": "iiko",
            "logs_mask_name": "MitsuCRPlugin",
            "serialNumber_key": "SERIAL='",
            "fnNumber_key": "FN='"
        }
    },
    "shtrihscanner": {
        "enabled": False,
        "exe_name": "shtrihscanner.exe"
    },
    "notification": {
        "enabled": False,
        "authentication": {
            "encryption": False,
            "bot_token": "",
            "chat_id": ""
        },
        "max_attempts": 5,
        "delay": 10
    }
}

ra_config = {
    "enabled": False,
    "id": "-",
    "temp_pass": "-"
}

def write_json_file(config, file_name):
    file_path = os.path.join(about.current_path, file_name)
    try:
        with open(file_path, "w", encoding="utf-8") as file:
            json.dump(config, file, ensure_ascii=False, indent=4)
        service.logger.logger_service.info(f"Данные записаны в '{file_name}'")
        service.logger.logger_service.debug(config)
    except Exception:
        service.logger.logger_service.error(f"Не удалось записать данные в '{file_path}'.", exc_info=True)

def read_config_file(folder_name, file_name, config_data, create=False):
    json_file = os.path.join(folder_name, file_name)
    try:
        with open(json_file, "r", encoding="utf-8") as file:
            config = json.load(file)
            return config
    except FileNotFoundError:
        service.logger.logger_service.warn(f"Файл конфига '{json_file}' отсутствует.")
        if create == True:
            create_json_file(folder_name, file_name, config_data)
    except json.JSONDecodeError:
        service.logger.logger_service.warn(f"Файл конфига '{json_file}' имеет некорректный формат данных")
        if create == True:
            create_json_file(folder_name, file_name, config_data)

def create_json_file(folder_name, file_name, data):
    json_file = os.path.join(folder_name, file_name)
    try:
        if not os.path.exists(folder_name):
            os.makedirs(folder_name)
        write_json_file(data, json_file)
    except Exception:
        service.logger.logger_service.error(f"Не удалось записать данные при создании файла: "
                                            f"'{json_file}'", exc_info=True)
