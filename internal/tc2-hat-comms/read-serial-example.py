import serial
import json

def main():
    ser = serial.Serial(
        port='/dev/ttyUSB0',
        baudrate=115200,
        timeout=1
    )

    print("Listening for serial messages... (Press Ctrl+C to stop)")

    try:
        while True:
            line = ser.readline().decode('utf-8').strip()

            if line:
                try:
                    data = json.loads(line)
                    message_type = data.get("type", "Unknown")
                    
                    if message_type == "classification":
                        data = data.get("data", {})
                        species = data.get("Species", {})
                        confidence = data.get("Confidence", "Unknown")
                        print(f"Type: {message_type}")
                        print(f"Species: {species}")
                        print(f"Confidence: {confidence}")
                        print("-" * 40)

                    else:
                        print("Unknown message type")
                        print(f"Type: {message_type}")
                        print(f"Data: {data}")
                        print("-" * 40)

                except json.JSONDecodeError:
                    print("Invalid JSON received:", line)

    except KeyboardInterrupt:
        print("\nExiting...")
    finally:
        ser.close()

if __name__ == "__main__":
    main()
