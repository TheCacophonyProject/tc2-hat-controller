#!/bin/bash

# Start the SSH agent
eval "$(ssh-agent -s)"
ssh_key_location="$HOME/.ssh/cacophony-pi"
ssh-add "$ssh_key_location"

# Discover Raspberry Pi services on the network
echo "Discovering Raspberry Pis with service _cacophonator-management._tcp..."
readarray -t services < <(avahi-browse -t -r _cacophonator-management._tcp | grep 'hostname' | awk '{print $3}' | sed 's/\[//' | sed 's/\]//' | sed 's/\.$/\.local/')

if [ ${#services[@]} -eq 0 ]; then
	echo "No Raspberry Pi services found on the network."
	exit 1
fi

# Display the discovered services
echo "Found Raspberry Pi services:"
for i in "${!services[@]}"; do
	echo "$((i + 1))) ${services[i]}"
done

# Let the user select a service
read -p "Select a Raspberry Pi to deploy to (1-${#services[@]}): " selection
pi_address=${services[$((selection - 1))]}

if [ -z "$pi_address" ]; then
	echo "Invalid selection."
	exit 1
fi

echo "Selected Raspberry Pi at: $pi_address"

while true; do
	# Build using the tc2-hat-controller build script
	echo "Building tc2-hat-controller..."
	./build.sh
	
	if [ $? -ne 0 ]; then
		echo "Error: Build failed"
		break
	fi

	# Find the built deb package
	deb_file=$(find dist -name "*.deb" -type f | head -n 1)
	
	if [ -z "$deb_file" ]; then
		echo "Error: No deb package found"
		break
	fi
	
	echo "Found deb package: $deb_file"
	
	# Deployment
	echo "Deploying to Raspberry Pi..."
	
	# Copy the deb file to the Raspberry Pi
	scp_command=("scp" "-i" "$ssh_key_location" "$deb_file" "pi@$pi_address:/home/pi/")
	echo "Executing: ${scp_command[*]}"
	"${scp_command[@]}"
	if [ $? -ne 0 ]; then
		echo "Error: SCP failed"
		break
	fi

	# Install the deb package
	deb_filename=$(basename "$deb_file")
	ssh_install_command=("ssh" "-i" "$ssh_key_location" "pi@$pi_address" "sudo dpkg -i /home/pi/$deb_filename")
	echo "Executing: ${ssh_install_command[*]}"
	"${ssh_install_command[@]}"
	if [ $? -ne 0 ]; then
		echo "Error: Package installation failed"
		break
	fi

	# Clean up the deb file from home directory
	ssh_cleanup_command=("ssh" "-i" "$ssh_key_location" "pi@$pi_address" "rm /home/pi/$deb_filename")
	echo "Executing: ${ssh_cleanup_command[*]}"
	"${ssh_cleanup_command[@]}"

	# Restart the tc2-hat-controller service
	ssh_restart_command=("ssh" "-i" "$ssh_key_location" "pi@$pi_address" "sudo systemctl restart tc2-hat-controller.service")
	echo "Executing: ${ssh_restart_command[*]}"
	"${ssh_restart_command[@]}"
	if [ $? -ne 0 ]; then
		echo "Error: Service restart failed"
		break
	fi

	# Stream logs from the service
	log_command=("ssh" "-i" "$ssh_key_location" "pi@$pi_address" "sudo journalctl -u tc2-hat-controller.service -f")
	echo "Streaming logs from tc2-hat-controller.service... (press Ctrl+C to stop)"
	"${log_command[@]}"

	echo "Deployment completed. Press any key to deploy again or Ctrl+C to exit."
	read -n 1 -s
	if [ $? -ne 0 ]; then
		break
	fi
	echo # new line for readability
done

# Kill the SSH agent when done
eval "$(ssh-agent -k)"