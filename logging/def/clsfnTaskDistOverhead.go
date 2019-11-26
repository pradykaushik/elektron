// Copyright (C) 2018 spdfg
// 
// This file is part of Elektron.
// 
// Elektron is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
// 
// Elektron is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
// 
// You should have received a copy of the GNU General Public License
// along with Elektron.  If not, see <http://www.gnu.org/licenses/>.
// 

package logging

type ClsfnTaskDistOverheadLogger struct {
	loggerObserverImpl
}

func (col ClsfnTaskDistOverheadLogger) Log(message string) {
	// Logging the overhead of classifying tasks in the scheduling window and determining the distribution
	//      of light power consuming and heavy power consuming tasks.
	col.logObserverSpecifics[clsfnTaskDistOverheadLogger].logFile.Println(message)
}