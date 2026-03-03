import os
import sys

service_name = "POSRelayd"
version = "2.4.8.3"

'''
в зависимости от того, как запускается служба, нужно менять переменную current_path

#отладочный путь под .py-скрипт
current_path = os.path.dirname(os.path.abspath(__file__))

#путь под собранный .exe-файл
current_path = os.path.dirname(sys.executable)
'''

#отладочный путь под .py-скрипт
current_path = os.path.dirname(os.path.abspath(__file__))